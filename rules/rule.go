package rules

import "context"

// Forwarder forwards a message to another user on behalf of the rules engine.
// Implement this interface and register it via WithForwarder to enable ActionForward.
type Forwarder interface {
	// Forward delivers a copy of messageID to toUserID.
	Forward(ctx context.Context, messageID, toUserID string) error
}

// RuleScope determines when a rule is evaluated.
type RuleScope string

const (
	// ScopeReceive applies the rule when a message is delivered to a recipient.
	ScopeReceive RuleScope = "receive"
	// ScopeSend applies the rule to the sender's copy after sending.
	ScopeSend RuleScope = "send"
)

// ActionType defines the type of action to perform when a rule matches.
type ActionType string

const (
	// ActionSetFolder moves the message to a specific folder.
	ActionSetFolder ActionType = "set_folder"
	// ActionAddTag adds a tag to the message.
	ActionAddTag ActionType = "add_tag"
	// ActionRemoveTag removes a tag from the message. Value is the tag ID.
	ActionRemoveTag ActionType = "remove_tag"
	// ActionMarkRead marks the message as read. Only valid for receive scope.
	ActionMarkRead ActionType = "mark_read"
	// ActionDelete soft-deletes the message (moves to trash). Only valid for receive scope.
	ActionDelete ActionType = "delete"
	// ActionHardDelete permanently removes the message. Only valid for receive scope.
	// Use with caution -- hard-deleted messages cannot be recovered.
	ActionHardDelete ActionType = "hard_delete"
	// ActionArchive moves the message to the archived folder.
	ActionArchive ActionType = "archive"
	// ActionSpam moves the message to the spam folder. Only valid for receive scope.
	ActionSpam ActionType = "spam"
	// ActionWebhook POSTs the message payload as JSON to the URL in Value.
	// Delivery is fire-and-forget; errors are logged but do not abort rule processing.
	ActionWebhook ActionType = "webhook"
	// ActionForward forwards the message to another user. Value is the target userID.
	// Requires a Forwarder configured via WithForwarder.
	ActionForward ActionType = "forward"
)

// validSendActions is the set of actions allowed on the send side.
var validSendActions = map[ActionType]bool{
	ActionSetFolder: true,
	ActionAddTag:    true,
	ActionRemoveTag: true,
	ActionArchive:   true,
	ActionWebhook:   true,
}

// isValidSendAction returns true if the action type is allowed on the send side.
func isValidSendAction(t ActionType) bool {
	return validSendActions[t]
}

// Action defines what to do when a rule matches.
type Action struct {
	// Type is the action to perform.
	Type ActionType
	// Value is the action parameter. Semantics depend on Type:
	//   SetFolder  — destination folder ID
	//   AddTag     — tag ID to add
	//   RemoveTag  — tag ID to remove
	//   Webhook    — URL to POST the message payload to
	//   Forward    — target user ID to forward the message to
	//   All others — ignored
	Value string
}

// Rule defines a single rule with a CEL condition and actions.
type Rule struct {
	// ID uniquely identifies the rule.
	ID string
	// Name is a human-readable label for the rule.
	Name string
	// Scope determines when the rule is evaluated (receive or send).
	// Defaults to ScopeReceive if empty.
	Scope RuleScope
	// Condition is a CEL expression that must evaluate to true for actions to apply.
	Condition string
	// Actions are applied in order when the condition matches.
	Actions []Action
	// Priority controls evaluation order. Lower values are evaluated first.
	Priority int
	// StopOnMatch stops evaluating further rules for this message when true.
	StopOnMatch bool
}

// effectiveScope returns the rule's scope, defaulting to ScopeReceive.
func (r Rule) effectiveScope() RuleScope {
	if r.Scope == "" {
		return ScopeReceive
	}
	return r.Scope
}

// RuleProvider supplies rules for evaluation. Implementations can be
// backed by a database, config file, or any other source.
type RuleProvider interface {
	// GlobalRules returns rules that apply to all users for the given scope.
	GlobalRules(ctx context.Context, scope RuleScope) ([]Rule, error)
	// UserRules returns rules for a specific user and scope.
	UserRules(ctx context.Context, userID string, scope RuleScope) ([]Rule, error)
}

// StaticRuleProvider is an in-memory RuleProvider for testing and simple use cases.
type StaticRuleProvider struct {
	global  []Rule
	perUser map[string][]Rule
}

// NewStaticProvider creates a StaticRuleProvider with the given global and per-user rules.
func NewStaticProvider(global []Rule, perUser map[string][]Rule) *StaticRuleProvider {
	if perUser == nil {
		perUser = make(map[string][]Rule)
	}
	return &StaticRuleProvider{
		global:  global,
		perUser: perUser,
	}
}

// GlobalRules returns global rules filtered by scope.
func (p *StaticRuleProvider) GlobalRules(_ context.Context, scope RuleScope) ([]Rule, error) {
	return filterByScope(p.global, scope), nil
}

// UserRules returns per-user rules filtered by scope.
func (p *StaticRuleProvider) UserRules(_ context.Context, userID string, scope RuleScope) ([]Rule, error) {
	return filterByScope(p.perUser[userID], scope), nil
}

func filterByScope(rules []Rule, scope RuleScope) []Rule {
	var out []Rule
	for _, r := range rules {
		if r.effectiveScope() == scope {
			out = append(out, r)
		}
	}
	return out
}
