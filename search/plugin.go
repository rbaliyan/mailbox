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
// It exposes event handlers for message received, moved, read, mark-all-read,
// and deleted events, and overrides the store's Search to delegate to the provider.
type Plugin struct {
	provider Provider
	st       store.Store
	opts     *options
}

// New creates a search Plugin wrapping the given provider and primary store.
// The returned store.Store overrides Search to delegate to the provider; pass it
// as the store to mailbox.New. The plugin must also be registered via
// mailbox.WithPlugin so that Init/Close and AfterSend are called by the service.
//
// Subscribe to events after the service is created:
//
//	svc.Events().MessageReceived.Subscribe(ctx, event.AsWorker(plugin.OnMessageReceived))
//	svc.Events().MessageDeleted.Subscribe(ctx, event.AsWorker(plugin.OnDelete))
//	svc.Events().MessageMoved.Subscribe(ctx, event.AsWorker(plugin.OnMessageMoved))
//	svc.Events().MessageRead.Subscribe(ctx, event.AsWorker(plugin.OnMessageRead))
//	svc.Events().MarkAllRead.Subscribe(ctx, event.AsWorker(plugin.OnMarkAllRead))
func New(provider Provider, st store.Store, opts ...Option) (*Plugin, store.Store) {
	o := &options{
		fallback: true,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	p := &Plugin{
		provider: provider,
		st:       st,
		opts:     o,
	}
	return p, &searchStore{Store: st, plugin: p}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "search:" + p.provider.Name() }

// Init connects the provider (creates index / applies settings) then pings it.
func (p *Plugin) Init(ctx context.Context) error {
	if err := p.provider.Connect(ctx); err != nil {
		return err
	}
	return p.provider.Ping(ctx)
}

// Close releases provider resources.
func (p *Plugin) Close(ctx context.Context) error {
	return p.provider.Close(ctx)
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
func (p *Plugin) OnMessageReceived(ctx context.Context, _ any, evt mailbox.MessageReceivedEvent) error {
	msg, err := p.st.Get(ctx, evt.MessageID)
	if err != nil {
		return err
	}
	return p.provider.Index(ctx, messageToDoc(msg))
}

// OnMessageMoved re-indexes a moved message to update its folder_id in the index.
// Register with svc.Events().MessageMoved.Subscribe(ctx, event.AsWorker(p.OnMessageMoved)).
func (p *Plugin) OnMessageMoved(ctx context.Context, _ any, evt mailbox.MessageMovedEvent) error {
	msg, err := p.st.Get(ctx, evt.MessageID)
	if err != nil {
		return err
	}
	return p.provider.Index(ctx, messageToDoc(msg))
}

// OnMessageRead re-indexes a message whose read status changed.
// Register with svc.Events().MessageRead.Subscribe(ctx, event.AsWorker(p.OnMessageRead)).
func (p *Plugin) OnMessageRead(ctx context.Context, _ any, evt mailbox.MessageReadEvent) error {
	msg, err := p.st.Get(ctx, evt.MessageID)
	if err != nil {
		return err
	}
	return p.provider.Index(ctx, messageToDoc(msg))
}

// OnMarkAllRead re-indexes all messages in the folder whose is_read status changed.
// Register with svc.Events().MarkAllRead.Subscribe(ctx, event.AsWorker(p.OnMarkAllRead)).
func (p *Plugin) OnMarkAllRead(ctx context.Context, _ any, evt mailbox.MarkAllReadEvent) error {
	const pageSize = 100
	var offset int
	for {
		list, err := p.st.Find(ctx, []store.Filter{
			store.OwnerIs(evt.UserID),
			store.InFolder(evt.FolderID),
		}, store.ListOptions{Limit: pageSize, Offset: offset})
		if err != nil {
			return err
		}
		for _, msg := range list.Messages {
			if indexErr := p.provider.Index(ctx, messageToDoc(msg)); indexErr != nil {
				p.opts.logger.WarnContext(ctx, "search: failed to re-index message on MarkAllRead",
					"message_id", msg.GetID(), "error", indexErr,
				)
			}
		}
		if !list.HasMore {
			break
		}
		offset += pageSize
	}
	return nil
}

// OnDelete removes a deleted message from the search index. Register this with
// svc.Events().MessageDeleted.Subscribe(ctx, event.AsWorker(p.OnDelete)).
func (p *Plugin) OnDelete(ctx context.Context, _ any, evt mailbox.MessageDeletedEvent) error {
	return p.provider.Delete(ctx, evt.MessageID)
}

// searchStore embeds store.Store and overrides Search to use the plugin provider.
type searchStore struct {
	store.Store
	plugin *Plugin
}

// Search queries the provider for matching IDs, fetches each message
// individually from the primary store, and returns them in provider relevance
// order. Fetching by ID breaks the taint path from the search query through
// to the primary-store query, since IDs are resolved through the external
// search provider (a network call) and are not raw user input.
// On provider error it falls back to the primary store's Search when
// WithFallback(true) (the default).
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

	// Fetch each message by ID in relevance order. The IDs come from the
	// external search provider (Meilisearch/Elasticsearch) and are already
	// scoped by owner_id at query time; we verify ownership here as a
	// defence-in-depth measure.
	messages := make([]store.Message, 0, len(ids))
	for _, id := range ids {
		msg, err := s.Get(ctx, id)
		if err != nil {
			s.plugin.opts.logger.WarnContext(ctx, "search: failed to fetch message by ID",
				"message_id", id, "error", err,
			)
			continue
		}
		if msg.GetOwnerID() != q.OwnerID {
			continue
		}
		messages = append(messages, msg)
	}

	return &store.MessageList{
		Messages: messages,
		Total:    int64(len(messages)),
	}, nil
}
