package mailbox

import (
	"context"
	"errors"
	"log/slog"

	"github.com/rbaliyan/mailbox/store"
)

// Plugin defines the interface for mailbox extensions.
// Plugins can hook into message sending to add custom behavior
// such as spam filtering, rate limiting, or content validation.
//
// For observing other operations (read, delete, archive, etc.),
// use the event system instead (EventMessageSent, EventMessageRead, etc.).
type Plugin interface {
	// Name returns the plugin identifier.
	Name() string
	// Init initializes the plugin. Called when service connects.
	Init(ctx context.Context) error
	// Close cleans up plugin resources. Called when service closes.
	Close(ctx context.Context) error
}

// SendHook is called before/after sending messages.
// This is the primary extension point for message validation and filtering.
type SendHook interface {
	Plugin
	// BeforeSend is called before a draft is sent. Return an error to abort.
	// Use this for spam filtering, rate limiting, or content validation.
	BeforeSend(ctx context.Context, userID string, draft store.DraftMessage) error
	// AfterSend is called after a message is successfully sent.
	// Return an error to signal post-send failures (e.g., notification errors).
	// Note: The message is already sent and cannot be rolled back.
	AfterSend(ctx context.Context, userID string, msg store.Message) error
}

// pluginRegistry holds registered plugins.
type pluginRegistry struct {
	all    []Plugin
	send   []SendHook
	logger *slog.Logger
}

// newPluginRegistry creates a new plugin registry.
func newPluginRegistry(logger *slog.Logger) *pluginRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	return &pluginRegistry{logger: logger}
}

// register adds a plugin to the registry.
func (r *pluginRegistry) register(p Plugin) {
	r.all = append(r.all, p)

	if h, ok := p.(SendHook); ok {
		r.send = append(r.send, h)
	}
}

// initAll initializes all plugins.
// On failure, already-initialized plugins are closed in reverse order.
func (r *pluginRegistry) initAll(ctx context.Context) error {
	for i, p := range r.all {
		if err := p.Init(ctx); err != nil {
			// Close already-initialized plugins in reverse order
			for j := i - 1; j >= 0; j-- {
				if closeErr := r.all[j].Close(ctx); closeErr != nil {
					r.logger.Error("failed to close plugin during init rollback",
						"plugin", r.all[j].Name(), "error", closeErr)
				}
			}
			return &PluginError{Plugin: p.Name(), Op: "init", Err: err}
		}
	}
	return nil
}

// closeAll closes all plugins in reverse order.
func (r *pluginRegistry) closeAll(ctx context.Context) error {
	var errs []error
	for i := len(r.all) - 1; i >= 0; i-- {
		if err := r.all[i].Close(ctx); err != nil {
			errs = append(errs, &PluginError{Plugin: r.all[i].Name(), Op: "close", Err: err})
		}
	}
	return errors.Join(errs...)
}

// PluginError represents an error from a plugin.
type PluginError struct {
	Plugin string
	Op     string
	Err    error
}

func (e *PluginError) Error() string {
	return "plugin " + e.Plugin + " " + e.Op + ": " + e.Err.Error()
}

func (e *PluginError) Unwrap() error {
	return e.Err
}

// Hook execution helpers

func (r *pluginRegistry) beforeSend(ctx context.Context, userID string, draft store.DraftMessage) error {
	for _, h := range r.send {
		if err := h.BeforeSend(ctx, userID, draft); err != nil {
			return &PluginError{Plugin: h.Name(), Op: "BeforeSend", Err: err}
		}
	}
	return nil
}

func (r *pluginRegistry) afterSend(ctx context.Context, userID string, msg store.Message) error {
	for _, h := range r.send {
		if err := h.AfterSend(ctx, userID, msg); err != nil {
			return &PluginError{Plugin: h.Name(), Op: "AfterSend", Err: err}
		}
	}
	return nil
}
