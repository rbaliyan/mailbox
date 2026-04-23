// Package meilisearch provides a Meilisearch-backed search.Provider.
package meilisearch

import (
	"context"
	"fmt"
	"time"

	ms "github.com/meilisearch/meilisearch-go"
	"github.com/rbaliyan/mailbox/search"
	"github.com/rbaliyan/mailbox/store"
)

// Compile-time check.
var _ search.Provider = (*Provider)(nil)

// document is the Meilisearch representation of a mailbox message.
// JSON field names are lowercase snake_case to match the filterable attribute names.
type document struct {
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

// Provider implements search.Provider using Meilisearch.
type Provider struct {
	client ms.ServiceManager
	opts   *options
}

// New creates a Meilisearch Provider. It creates the index and applies
// searchable/filterable attribute settings on construction.
func New(opts ...Option) (*Provider, error) {
	o := &options{
		host:  "http://localhost:7700",
		index: "mailbox_messages",
		searchableFields: []string{
			"subject",
			"body",
		},
		filterableFields: []string{
			"owner_id",
			"folder_id",
			"tags",
			"is_read",
		},
	}
	for _, opt := range opts {
		opt(o)
	}

	var msOpts []ms.Option
	if o.apiKey != "" {
		msOpts = append(msOpts, ms.WithAPIKey(o.apiKey))
	}
	client := ms.New(o.host, msOpts...)

	p := &Provider{client: client, opts: o}

	// Ensure the index exists with the configured primary key.
	ctx := context.Background()
	if _, err := client.CreateIndexWithContext(ctx, &ms.IndexConfig{
		Uid:        o.index,
		PrimaryKey: "id",
	}); err != nil {
		// Ignore "index already exists" — a previous instance may have created it.
		if msErr, ok := err.(*ms.Error); !ok || msErr.StatusCode != 409 {
			return nil, fmt.Errorf("meilisearch: create index: %w", err)
		}
	}

	// Apply searchable and filterable attribute settings.
	idx := client.Index(o.index)
	if _, err := idx.UpdateSettingsWithContext(ctx, &ms.Settings{
		SearchableAttributes: o.searchableFields,
		FilterableAttributes: o.filterableFields,
	}); err != nil {
		return nil, fmt.Errorf("meilisearch: update settings: %w", err)
	}

	return p, nil
}

// Name returns the provider identifier.
func (p *Provider) Name() string { return "meilisearch" }

// Ping checks connectivity to the Meilisearch server.
func (p *Provider) Ping(ctx context.Context) error {
	resp, err := p.client.HealthWithContext(ctx)
	if err != nil {
		return fmt.Errorf("meilisearch: ping: %w", err)
	}
	if resp.Status != "available" {
		return fmt.Errorf("meilisearch: ping: server status %q", resp.Status)
	}
	return nil
}

// Index upserts a document into the Meilisearch index.
func (p *Provider) Index(ctx context.Context, doc search.Document) error {
	d := document{
		ID:        doc.ID,
		OwnerID:   doc.OwnerID,
		SenderID:  doc.SenderID,
		Subject:   doc.Subject,
		Body:      doc.Body,
		FolderID:  doc.FolderID,
		Tags:      doc.Tags,
		IsRead:    doc.IsRead,
		CreatedAt: doc.CreatedAt,
	}
	if _, err := p.client.Index(p.opts.index).AddDocumentsWithContext(ctx, []*document{&d}, nil); err != nil {
		return fmt.Errorf("meilisearch: index: %w", err)
	}
	return nil
}

// Delete removes a document from the Meilisearch index by message ID.
func (p *Provider) Delete(ctx context.Context, messageID string) error {
	if _, err := p.client.Index(p.opts.index).DeleteDocumentWithContext(ctx, messageID, nil); err != nil {
		return fmt.Errorf("meilisearch: delete: %w", err)
	}
	return nil
}

// Search queries Meilisearch and returns message IDs in relevance order.
// Results are always scoped to the ownerID.
func (p *Provider) Search(ctx context.Context, q store.SearchQuery) ([]string, error) {
	filter := fmt.Sprintf("owner_id = %q", q.OwnerID)

	req := &ms.SearchRequest{
		Filter:               filter,
		AttributesToRetrieve: []string{"id"},
	}
	if q.Options.Limit > 0 {
		req.Limit = int64(q.Options.Limit)
	}
	if q.Options.Offset > 0 {
		req.Offset = int64(q.Options.Offset)
	}

	resp, err := p.client.Index(p.opts.index).SearchWithContext(ctx, q.Query, req)
	if err != nil {
		return nil, fmt.Errorf("meilisearch: search: %w", err)
	}

	ids := make([]string, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		var d document
		if err := hit.DecodeInto(&d); err != nil {
			continue
		}
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}

// Close is a no-op; the Meilisearch HTTP client has no explicit close.
func (p *Provider) Close() error {
	p.client.Close()
	return nil
}
