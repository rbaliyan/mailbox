package search

import (
	"context"
	"log/slog"

	mailbox "github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/store"
)

// Compile-time interface checks.
var _ mailbox.Plugin = (*Plugin)(nil)
var _ mailbox.SendHook = (*Plugin)(nil)

// Plugin wraps a Provider as a mailbox.Plugin and mailbox.SendHook.
// It also exposes event handlers for message received and deleted events,
// and a WrapStore method that returns a store.Store whose Search method
// delegates to the provider.
type Plugin struct {
	provider Provider
	st       store.Store // set by WrapStore; required for event-based indexing
	opts     *options
}

// New creates a new search Plugin wrapping the given provider.
func New(provider Provider, opts ...Option) *Plugin {
	o := &options{
		fallback: true,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return &Plugin{
		provider: provider,
		opts:     o,
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "search:" + p.provider.Name() }

// Init pings the provider to verify connectivity.
func (p *Plugin) Init(ctx context.Context) error {
	return p.provider.Ping(ctx)
}

// Close releases provider resources.
func (p *Plugin) Close(_ context.Context) error {
	return p.provider.Close()
}

// BeforeSend satisfies mailbox.SendHook. No pre-send action is needed.
func (p *Plugin) BeforeSend(_ context.Context, _ string, _ store.DraftMessage) error {
	return nil
}

// AfterSend indexes the message in the search provider after a successful send.
func (p *Plugin) AfterSend(ctx context.Context, _ string, msg store.Message) error {
	return p.provider.Index(ctx, messageToDoc(msg))
}

// OnMessageReceived indexes a received message. Register this with
// svc.Events().MessageReceived.Subscribe(ctx, event.AsWorker(p.OnMessageReceived)).
//
// When WrapStore has not been called, indexing is skipped with a warning because
// the full message cannot be fetched.
func (p *Plugin) OnMessageReceived(ctx context.Context, _ any, evt mailbox.MessageReceivedEvent) error {
	if p.st == nil {
		p.opts.logger.WarnContext(ctx, "search: WrapStore not called, skipping index on MessageReceived",
			"message_id", evt.MessageID,
		)
		return nil
	}
	msg, err := p.st.Get(ctx, evt.MessageID)
	if err != nil {
		return err
	}
	return p.provider.Index(ctx, messageToDoc(msg))
}

// OnDelete removes a deleted message from the search index. Register this with
// svc.Events().MessageDeleted.Subscribe(ctx, event.AsWorker(p.OnDelete)).
func (p *Plugin) OnDelete(ctx context.Context, _ any, evt mailbox.MessageDeletedEvent) error {
	return p.provider.Delete(ctx, evt.MessageID)
}

// WrapStore returns a store.Store that uses the plugin's provider for Search
// queries, falling back to the primary store on error when WithFallback(true)
// (the default). Call this before constructing the mailbox.Service.
func (p *Plugin) WrapStore(s store.Store) store.Store {
	p.st = s
	return &searchStore{Store: s, plugin: p}
}

// searchStore embeds store.Store and overrides Search to use the plugin provider.
type searchStore struct {
	store.Store
	plugin *Plugin
}

// Search queries the provider for matching IDs, fetches the full messages from
// the primary store, and returns them in provider relevance order. On provider
// error it falls back to the primary store's Search when fallback is enabled.
func (s *searchStore) Search(ctx context.Context, q store.SearchQuery) (*store.MessageList, error) {
	ids, err := s.plugin.provider.Search(ctx, q)
	if err != nil {
		if s.plugin.opts.fallback {
			s.plugin.opts.logger.WarnContext(ctx, "search: provider error, falling back to primary store",
				"error", err,
			)
			return s.Store.Search(ctx, q)
		}
		return nil, err
	}

	if len(ids) == 0 {
		return &store.MessageList{}, nil
	}

	// Build an ID-in filter and fetch full messages.
	anyIDs := make([]any, len(ids))
	for i, id := range ids {
		anyIDs[i] = id
	}
	idFilter, err := store.MessageFilter("id").In(anyIDs...)
	if err != nil {
		if s.plugin.opts.fallback {
			return s.Store.Search(ctx, q)
		}
		return nil, err
	}

	list, err := s.Find(ctx, []store.Filter{store.OwnerIs(q.OwnerID), idFilter}, store.ListOptions{Limit: len(ids)})
	if err != nil {
		if s.plugin.opts.fallback {
			return s.Store.Search(ctx, q)
		}
		return nil, err
	}

	// Reorder messages to match the provider's relevance ordering.
	byID := make(map[string]store.Message, len(list.Messages))
	for _, msg := range list.Messages {
		byID[msg.GetID()] = msg
	}
	ordered := make([]store.Message, 0, len(ids))
	for _, id := range ids {
		if msg, ok := byID[id]; ok {
			ordered = append(ordered, msg)
		}
	}

	return &store.MessageList{
		Messages: ordered,
		Total:    int64(len(ordered)),
		HasMore:  false,
	}, nil
}
