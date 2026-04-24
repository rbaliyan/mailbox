// Package search provides a pluggable full-text search layer for the mailbox library.
//
// Usage:
//
//	provider, _ := meilisearch.New(meilisearch.WithHost("http://localhost:7700"), meilisearch.WithAPIKey("key"))
//	plugin, wrappedStore := search.New(provider, myStore)
//	svc, _ := mailbox.New(cfg, mailbox.WithStore(wrappedStore), mailbox.WithPlugin(plugin))
//	svc.Events().MessageReceived.Subscribe(ctx, event.AsWorker(plugin.OnMessageReceived))
//	svc.Events().MessageDeleted.Subscribe(ctx, event.AsWorker(plugin.OnDelete))
//	svc.Events().MessageMoved.Subscribe(ctx, event.AsWorker(plugin.OnMessageMoved))
//	svc.Events().MessageRead.Subscribe(ctx, event.AsWorker(plugin.OnMessageRead))
//	svc.Events().MarkAllRead.Subscribe(ctx, event.AsWorker(plugin.OnMarkAllRead))
package search

import (
	"context"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// Provider is the search backend interface. Implementations must be safe for concurrent use.
type Provider interface {
	// Name identifies the backend (e.g., "meilisearch", "elasticsearch").
	Name() string
	// Connect initializes the backend (creates index, applies settings).
	// Called by Plugin.Init; the ctx deadline controls the setup timeout.
	Connect(ctx context.Context) error
	// Index upserts a document. Called after send and on message received/moved/read.
	Index(ctx context.Context, doc Document) error
	// Delete removes a document by message ID.
	Delete(ctx context.Context, messageID string) error
	// Search returns message IDs matching q, in relevance order.
	// OwnerID is always set; implementations must scope results to it.
	Search(ctx context.Context, q store.SearchQuery) ([]string, error)
	// Ping checks backend connectivity. Called during Init after Connect.
	Ping(ctx context.Context) error
	// Close releases resources.
	Close(ctx context.Context) error
}

// Document is a flat message representation for indexing.
type Document struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"owner_id"`
	SenderID  string    `json:"sender_id"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	FolderID  string    `json:"folder_id"`
	Tags      []string  `json:"tags"`
	IsRead    bool      `json:"is_read"`
	CreatedAt time.Time `json:"created_at"`
}

// messageToDoc converts a store.Message to a Document for indexing.
func messageToDoc(msg store.Message) Document {
	return Document{
		ID:        msg.GetID(),
		OwnerID:   msg.GetOwnerID(),
		SenderID:  msg.GetSenderID(),
		Subject:   msg.GetSubject(),
		Body:      msg.GetBody(),
		FolderID:  msg.GetFolderID(),
		Tags:      msg.GetTags(),
		IsRead:    msg.GetIsRead(),
		CreatedAt: msg.GetCreatedAt(),
	}
}
