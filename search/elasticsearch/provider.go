// Package elasticsearch provides an Elasticsearch-backed search.Provider.
package elasticsearch

import (
	"context"
	"fmt"
	"time"

	eslib "github.com/elastic/go-elasticsearch/v8"
	essearch "github.com/elastic/go-elasticsearch/v8/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	mailsearch "github.com/rbaliyan/mailbox/search"
	"github.com/rbaliyan/mailbox/store"
)

// Compile-time check.
var _ mailsearch.Provider = (*Provider)(nil)

// esDocument is the Elasticsearch representation of a mailbox message.
// JSON tags use the same snake_case names as the Meilisearch provider for consistency.
type esDocument struct {
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

// Provider implements mailsearch.Provider using Elasticsearch.
type Provider struct {
	client *eslib.TypedClient
	opts   *options
}

// New creates an Elasticsearch Provider. Call Connect(ctx) (via Plugin.Init) to
// ensure the target index exists before use.
func New(opts ...Option) (*Provider, error) {
	o := &options{
		index: "mailbox_messages",
	}
	for _, opt := range opts {
		opt(o)
	}

	cfg := eslib.Config{
		Addresses: o.addresses,
		Username:  o.username,
		Password:  o.password,
		APIKey:    o.apiKey,
		CloudID:   o.cloudID,
	}

	client, err := eslib.NewTypedClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: create client: %w", err)
	}

	return &Provider{client: client, opts: o}, nil
}

// Name returns the provider identifier.
func (p *Provider) Name() string { return "elasticsearch" }

// Connect ensures the target index exists. Called by Plugin.Init.
func (p *Provider) Connect(ctx context.Context) error {
	exists, err := p.client.Indices.Exists(p.opts.index).Do(ctx)
	if err != nil {
		return fmt.Errorf("elasticsearch: check index: %w", err)
	}
	if !exists {
		if _, err := p.client.Indices.Create(p.opts.index).Do(ctx); err != nil {
			return fmt.Errorf("elasticsearch: create index: %w", err)
		}
	}
	return nil
}

// Ping checks cluster connectivity.
func (p *Provider) Ping(ctx context.Context) error {
	ok, err := p.client.Ping().Do(ctx)
	if err != nil {
		return fmt.Errorf("elasticsearch: ping: %w", err)
	}
	if !ok {
		return fmt.Errorf("elasticsearch: ping: cluster not reachable")
	}
	return nil
}

// Index upserts a document into the Elasticsearch index.
func (p *Provider) Index(ctx context.Context, doc mailsearch.Document) error {
	d := esDocument{
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
	if _, err := p.client.Index(p.opts.index).Id(doc.ID).Document(d).Do(ctx); err != nil {
		return fmt.Errorf("elasticsearch: index: %w", err)
	}
	return nil
}

// Delete removes a document from the Elasticsearch index by message ID.
func (p *Provider) Delete(ctx context.Context, messageID string) error {
	if _, err := p.client.Delete(p.opts.index, messageID).Do(ctx); err != nil {
		return fmt.Errorf("elasticsearch: delete: %w", err)
	}
	return nil
}

// Search queries Elasticsearch and returns message IDs in relevance order.
// Results are always scoped to q.OwnerID using a bool query filter.
func (p *Provider) Search(ctx context.Context, q store.SearchQuery) ([]string, error) {
	boolQuery := types.NewBoolQuery()
	boolQuery.Filter = []types.Query{
		{
			Term: map[string]types.TermQuery{
				"owner_id": {Value: q.OwnerID},
			},
		},
	}
	if q.Query != "" {
		boolQuery.Must = []types.Query{
			{
				MultiMatch: &types.MultiMatchQuery{
					Query:  q.Query,
					Fields: []string{"subject", "body"},
				},
			},
		}
	}

	size := q.Options.Limit
	if size <= 0 {
		size = 50
	}
	from := q.Options.Offset

	req := &essearch.Request{
		Query: &types.Query{Bool: boolQuery},
		Size:  &size,
	}
	if from > 0 {
		req.From = &from
	}

	resp, err := p.client.Search().Index(p.opts.index).Request(req).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: search: %w", err)
	}

	ids := make([]string, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		if hit.Id_ != nil {
			ids = append(ids, *hit.Id_)
		}
	}
	return ids, nil
}

// Close is a no-op; the Elasticsearch HTTP client has no explicit close.
func (p *Provider) Close(_ context.Context) error { return nil }
