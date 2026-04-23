package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/rbaliyan/mailbox/notify"
)

// Compile-time interface check.
var _ notify.Router = (*Router)(nil)

// Router implements notify.Router by POSTing JSON-encoded events to configured
// HTTP endpoints. It delivers to all endpoints whose event filter matches,
// regardless of the RoutingInfo passed by the notifier.
type Router struct {
	client *http.Client
	opts   *options
}

// New creates a new webhook Router.
func New(opts ...Option) *Router {
	o := &options{
		timeout:    defaultTimeout,
		maxRetries: defaultMaxRetries,
		initDelay:  defaultInitDelay,
		maxDelay:   defaultMaxDelay,
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return &Router{
		client: &http.Client{Timeout: o.timeout},
		opts:   o,
	}
}

// Route delivers the event to all configured endpoints whose event filter matches.
// RoutingInfo is ignored — webhook delivery is always broadcast to all endpoints.
//
// If no endpoints match the event type, Route returns nil (nothing to do).
// If at least one endpoint matches and all of them fail after retries, Route
// returns a combined error so the notifier can fall back to store persistence.
// Partial success (some endpoints succeed, others fail) returns nil; failures
// are logged at Warn level.
func (r *Router) Route(ctx context.Context, _ notify.RoutingInfo, evt notify.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("webhook: marshal event: %w", err)
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	var (
		targeted int
		failures []error
	)
	for _, ep := range r.opts.endpoints {
		if !r.matches(ep, evt.Type) {
			continue
		}
		targeted++
		if err := r.deliver(ctx, ep, payload, timestamp); err != nil {
			r.opts.logger.Warn("webhook delivery failed",
				"url", ep.URL,
				"event_type", evt.Type,
				"error", err,
			)
			failures = append(failures, fmt.Errorf("%s: %w", ep.URL, err))
		}
	}

	// No endpoints matched — nothing to do, not an error.
	if targeted == 0 {
		return nil
	}
	// All targeted endpoints failed — surface the error so the notifier can
	// fall back to store persistence.
	if len(failures) == targeted {
		return fmt.Errorf("webhook: all %d endpoints failed: %w", targeted, errors.Join(failures...))
	}
	// Partial success — already logged above.
	return nil
}

// nonRetryableError wraps errors that must not trigger a retry (e.g. HTTP 4xx).
type nonRetryableError struct{ err error }

func (e *nonRetryableError) Error() string { return e.err.Error() }
func (e *nonRetryableError) Unwrap() error { return e.err }

// deliver sends the payload to a single endpoint with retries.
// 4xx responses are not retried — they indicate a permanent endpoint misconfiguration.
func (r *Router) deliver(ctx context.Context, ep EndpointConfig, payload []byte, timestamp string) error {
	delay := r.opts.initDelay
	var lastErr error

	for attempt := 0; attempt <= r.opts.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay = min(delay*2, r.opts.maxDelay) //nolint:predeclared
		}

		if err := r.post(ctx, ep, payload, timestamp); err != nil {
			lastErr = err
			var nre *nonRetryableError
			if errors.As(err, &nre) {
				return err // do not burn retry budget on permanent failures
			}
			continue
		}
		return nil
	}
	return lastErr
}

// post executes a single HTTP POST attempt.
func (r *Router) post(ctx context.Context, ep EndpointConfig, payload []byte, timestamp string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mailbox-Timestamp", timestamp)

	if len(r.opts.signingKey) > 0 {
		sig := sign(r.opts.signingKey, timestamp, payload)
		req.Header.Set("X-Mailbox-Signature", "sha256="+sig)
	}

	for k, v := range ep.Headers {
		req.Header.Set(k, v)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	// Drain before closing so the underlying TCP connection can be reused.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("server error: %d", resp.StatusCode)
	}
	// 4xx signals endpoint misconfiguration. Surface as non-retryable so deliver()
	// stops immediately without burning the retry budget.
	if resp.StatusCode >= 400 {
		return &nonRetryableError{fmt.Errorf("client error: %d", resp.StatusCode)}
	}
	return nil
}

// matches reports whether an endpoint should receive an event of the given type.
func (r *Router) matches(ep EndpointConfig, eventType string) bool {
	if len(ep.Events) == 0 {
		return true
	}
	return slices.Contains(ep.Events, eventType)
}

// sign computes the HMAC-SHA256 signature over "timestamp.payload".
func sign(key []byte, timestamp string, payload []byte) string {
	msg := append([]byte(timestamp+"."), payload...)
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature verifies an incoming webhook request's HMAC-SHA256 signature
// and rejects requests whose timestamp falls outside the given tolerance window.
// sigHeader should be the raw value of the X-Mailbox-Signature header (e.g. "sha256=abc...").
// Use a tolerance of 5*time.Minute to match industry convention (Stripe, GitHub, Slack).
// Pass 0 to skip the timestamp check (not recommended for production).
func VerifySignature(secret []byte, timestamp string, body []byte, sigHeader string, tolerance time.Duration) bool {
	const prefix = "sha256="
	// Reject any header whose length is <= the prefix. This covers the empty
	// string, shorter-than-prefix values, and the exact-prefix case
	// ("sha256=" with no digest) — all must fail verification.
	if len(sigHeader) <= len(prefix) {
		return false
	}
	// Reject empty signing keys — an empty key produces a deterministic HMAC
	// that an attacker can forge without knowing the real secret.
	if len(secret) == 0 {
		return false
	}
	// Enforce freshness to prevent replay attacks.
	if tolerance > 0 {
		ts, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil {
			return false
		}
		age := math.Abs(float64(time.Now().Unix() - ts))
		if time.Duration(age)*time.Second > tolerance {
			return false
		}
	}
	expected := sign(secret, timestamp, body)
	return hmac.Equal([]byte(sigHeader[len(prefix):]), []byte(expected))
}
