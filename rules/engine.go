package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/rbaliyan/mailbox/store"
)

// DefaultMaxCacheSize is the default maximum number of compiled CEL programs to cache.
const DefaultMaxCacheSize = 1000

// defaultWebhookTimeout is the HTTP timeout for fire-and-forget webhook deliveries.
// It uses a detached context so the caller's cancellation does not abort the request.
const defaultWebhookTimeout = 10 * time.Second

// Engine evaluates CEL rules and applies actions via store operations.
// It implements the mailbox.Plugin and mailbox.SendHook interfaces.
type Engine struct {
	provider RuleProvider
	store    store.Store
	env      *cel.Env
	cache    map[string]cel.Program
	mu       sync.RWMutex
	logger   *slog.Logger
	opts     engineOptions
}

// NewEngine creates a rules engine with the given provider and store.
// The store is used to apply post-creation mutations (MoveToFolder, AddTag, etc.).
func NewEngine(provider RuleProvider, st store.Store, opts ...Option) (*Engine, error) {
	if provider == nil {
		return nil, fmt.Errorf("rules: provider must not be nil")
	}
	if st == nil {
		return nil, fmt.Errorf("rules: store must not be nil")
	}

	env, err := newCELEnv()
	if err != nil {
		return nil, fmt.Errorf("rules: create CEL environment: %w", err)
	}

	o := engineOptions{
		logger:       slog.Default(),
		maxCacheSize: DefaultMaxCacheSize,
	}
	for _, opt := range opts {
		opt(&o)
	}

	return &Engine{
		provider: provider,
		store:    st,
		env:      env,
		cache:    make(map[string]cel.Program),
		logger:   o.logger,
		opts:     o,
	}, nil
}

// Name returns the plugin identifier.
func (e *Engine) Name() string { return "rules" }

// Init initializes the plugin. The CEL environment is created in the constructor.
func (e *Engine) Init(_ context.Context) error { return nil }

// Close clears the compiled program cache.
func (e *Engine) Close(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache = make(map[string]cel.Program)
	return nil
}

// BeforeSend is a no-op. Rules do not block message sending.
func (e *Engine) BeforeSend(_ context.Context, _ string, _ store.DraftMessage) error {
	return nil
}

// AfterSend evaluates send-scope rules against the sender's message copy
// and applies valid send-side actions (SetFolder, AddTag, Archive).
func (e *Engine) AfterSend(ctx context.Context, userID string, msg store.Message) error {
	rules, err := e.fetchRules(ctx, userID, ScopeSend)
	if err != nil {
		return e.handleError("fetch send rules", err)
	}
	if len(rules) == 0 {
		return nil
	}

	activation := buildActivation(msg)
	for _, rule := range rules {
		matched, evalErr := e.evaluate(rule, activation)
		if evalErr != nil {
			if err := e.handleError("evaluate rule "+rule.ID, evalErr); err != nil {
				return err
			}
			continue
		}
		if !matched {
			continue
		}

		if err := e.applySendActions(ctx, msg.GetID(), rule.Actions); err != nil {
			return e.handleError("apply send actions for rule "+rule.ID, err)
		}

		if rule.StopOnMatch {
			break
		}
	}
	return nil
}

// OnMessageReceived handles MessageReceived events and evaluates receive-scope rules.
// Register this as an event handler on the service's MessageReceived event.
//
// The handler signature matches event.Handler[mailbox.MessageReceivedEvent].
// The second parameter is the event instance; it is unused here.
func (e *Engine) OnMessageReceived(ctx context.Context, _ any, data MessageReceivedData) error {
	msg, err := e.store.Get(ctx, data.MessageID)
	if err != nil {
		return fmt.Errorf("rules: get message %s: %w", data.MessageID, err)
	}

	// Verify the message belongs to the recipient to prevent cross-user mutations.
	if msg.GetOwnerID() != data.RecipientID {
		return fmt.Errorf("rules: message %s not owned by recipient", data.MessageID)
	}

	rules, err := e.fetchRules(ctx, data.RecipientID, ScopeReceive)
	if err != nil {
		return e.handleError("fetch receive rules", err)
	}
	if len(rules) == 0 {
		return nil
	}

	activation := buildActivation(msg)
	for _, rule := range rules {
		matched, evalErr := e.evaluate(rule, activation)
		if evalErr != nil {
			if err := e.handleError("evaluate rule "+rule.ID, evalErr); err != nil {
				return err
			}
			continue
		}
		if !matched {
			continue
		}

		if err := e.applyReceiveActions(ctx, data.MessageID, rule.Actions); err != nil {
			return e.handleError("apply receive actions for rule "+rule.ID, err)
		}

		if rule.StopOnMatch {
			break
		}
	}
	return nil
}

// MessageReceivedData contains the fields needed by OnMessageReceived.
// This matches the shape of mailbox.MessageReceivedEvent without importing
// the parent package (avoiding a circular dependency).
type MessageReceivedData struct {
	MessageID   string
	RecipientID string
}

// fetchRules retrieves and sorts global + user rules for the given scope.
func (e *Engine) fetchRules(ctx context.Context, userID string, scope RuleScope) ([]Rule, error) {
	globalRules, err := e.provider.GlobalRules(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("global rules: %w", err)
	}

	userRules, err := e.provider.UserRules(ctx, userID, scope)
	if err != nil {
		return nil, fmt.Errorf("user rules: %w", err)
	}

	combined := make([]Rule, 0, len(globalRules)+len(userRules))
	combined = append(combined, globalRules...)
	combined = append(combined, userRules...)

	sort.Slice(combined, func(i, j int) bool {
		return combined[i].Priority < combined[j].Priority
	})

	return combined, nil
}

// evaluate compiles (or retrieves from cache) and evaluates a rule's CEL expression.
func (e *Engine) evaluate(rule Rule, activation map[string]any) (bool, error) {
	prg, err := e.compileOrCache(rule)
	if err != nil {
		return false, fmt.Errorf("compile %q: %w", rule.Condition, err)
	}

	out, _, err := prg.Eval(activation)
	if err != nil {
		return false, fmt.Errorf("eval %q: %w", rule.Condition, err)
	}

	result, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("rule %s: condition must evaluate to bool, got %T", rule.ID, out.Value())
	}
	return result, nil
}

// compileOrCache returns a compiled CEL program, using the cache when possible.
func (e *Engine) compileOrCache(rule Rule) (cel.Program, error) {
	key := rule.ID + "\x00" + rule.Condition

	e.mu.RLock()
	if prg, ok := e.cache[key]; ok {
		e.mu.RUnlock()
		return prg, nil
	}
	e.mu.RUnlock()

	ast, iss := e.env.Compile(rule.Condition)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}

	prg, err := e.env.Program(ast)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	// Re-check after acquiring write lock to avoid redundant inserts.
	if existing, ok := e.cache[key]; ok {
		e.mu.Unlock()
		return existing, nil
	}
	// Evict all entries when cache is full. Simple but effective for a
	// bounded cache; rules typically stabilize quickly.
	if e.opts.maxCacheSize > 0 && len(e.cache) >= e.opts.maxCacheSize {
		e.cache = make(map[string]cel.Program)
	}
	e.cache[key] = prg
	e.mu.Unlock()

	return prg, nil
}

// applySendActions applies only send-valid actions to a message.
func (e *Engine) applySendActions(ctx context.Context, messageID string, actions []Action) error {
	for _, a := range actions {
		if !isValidSendAction(a.Type) {
			continue
		}
		if err := e.applyAction(ctx, messageID, a); err != nil {
			return err
		}
	}
	return nil
}

// applyReceiveActions applies all actions to a message.
func (e *Engine) applyReceiveActions(ctx context.Context, messageID string, actions []Action) error {
	for _, a := range actions {
		if err := e.applyAction(ctx, messageID, a); err != nil {
			return err
		}
	}
	return nil
}

// applyAction applies a single action to a message via store operations.
func (e *Engine) applyAction(ctx context.Context, messageID string, action Action) error {
	switch action.Type {
	case ActionSetFolder:
		return e.store.MoveToFolder(ctx, messageID, action.Value)
	case ActionAddTag:
		return e.store.AddTag(ctx, messageID, action.Value)
	case ActionRemoveTag:
		return e.store.RemoveTag(ctx, messageID, action.Value)
	case ActionMarkRead:
		return e.store.MarkRead(ctx, messageID, true)
	case ActionDelete:
		return e.store.Delete(ctx, messageID)
	case ActionHardDelete:
		return e.store.HardDelete(ctx, messageID)
	case ActionArchive:
		return e.store.MoveToFolder(ctx, messageID, store.FolderArchived)
	case ActionSpam:
		return e.store.MoveToFolder(ctx, messageID, store.FolderSpam)
	case ActionWebhook:
		e.fireWebhook(ctx, messageID, action.Value)
		return nil
	case ActionForward:
		if e.opts.forwarder == nil {
			e.logger.Warn("ActionForward requires a Forwarder; set WithForwarder", "message_id", messageID)
			return nil
		}
		return e.opts.forwarder.Forward(ctx, messageID, action.Value)
	default:
		e.logger.Warn("unknown action type", "type", action.Type, "message_id", messageID)
		if e.opts.strictMode {
			return fmt.Errorf("rules: unknown action type %q", action.Type)
		}
		return nil
	}
}

// fireWebhook posts the message ID as JSON to the given URL.
// This is fire-and-forget: errors are logged but not returned.
//
// The HTTP call uses a detached context with its own timeout rather than the
// caller's context. The caller's context is typically an event handler scope
// that is cancelled as soon as the handler returns, which would race the HTTP
// request.
func (e *Engine) fireWebhook(_ context.Context, messageID, rawURL string) {
	if err := validateWebhookURL(rawURL); err != nil {
		e.logger.Warn("rules webhook: invalid url, skipping", "url", rawURL, "error", err)
		return
	}

	payload, err := json.Marshal(map[string]string{
		"message_id": messageID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		e.logger.Error("rules webhook: marshal payload", "error", err)
		return
	}

	client := e.opts.httpClient
	if client == nil {
		client = defaultWebhookClient
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), defaultWebhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		e.logger.Error("rules webhook: create request", "url", rawURL, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		e.logger.Warn("rules webhook: delivery failed", "url", rawURL, "error", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		e.logger.Warn("rules webhook: non-success response", "url", rawURL, "status", resp.StatusCode)
	}
}

// defaultWebhookClient is a hardened HTTP client for fire-and-forget webhook delivery.
// Redirects are disabled to prevent SSRF via redirect chains.
var defaultWebhookClient = &http.Client{
	Timeout: defaultWebhookTimeout,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse // never follow redirects
	},
}

// validateWebhookURL rejects URLs that could be used to reach internal services
// (SSRF). Only http and https schemes are allowed, and the resolved IP must not
// be loopback, private, or link-local.
func validateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q: only http/https allowed", u.Scheme)
	}
	host := u.Hostname()
	addrs, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("dns lookup: %w", err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("webhook url resolves to internal address: %s", addr)
		}
	}
	return nil
}

// handleError logs the error and returns it only if strict mode is enabled.
func (e *Engine) handleError(op string, err error) error {
	e.logger.Error("rules engine error", "op", op, "error", err)
	if e.opts.strictMode {
		return fmt.Errorf("rules: %s: %w", op, err)
	}
	return nil
}
