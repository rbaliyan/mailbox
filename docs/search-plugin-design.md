# Search Plugin Design for Mailbox Library

## Overview

This document describes the design for a plugin-based search interface that can work with multiple search backends: MongoDB Atlas Search, Elasticsearch, Meilisearch, and custom implementations.

### Scope

The search plugin is designed for **full-text search functionality only**. The following features are explicitly **out of scope**:
- Autocomplete / type-ahead suggestions
- Search-as-you-type
- Query suggestions

These features require different infrastructure (prefix indexes, n-gram analyzers) and would significantly complicate the interface without adding core value to the messaging use case.

## Current State Analysis

The mailbox library currently has:
- A basic `SearchQuery` interface with text search (subject/body) using MongoDB regex
- Limited search capabilities (no full-text search, faceting, or highlighting)
- A plugin architecture for lifecycle hooks but no search-specific plugins
- Message fields suitable for sophisticated searching: ID, OwnerID, SenderID, RecipientIDs, Subject, Body, Metadata, Status, IsRead, FolderID, Tags, Attachments, CreatedAt, UpdatedAt

## Proposed Plugin-Based Search Architecture

### 1. Core Search Provider Interface

```go
package search

import (
    "context"
    "time"
    "github.com/rbaliyan/mailbox/store"
)

// Provider defines the search backend interface
type Provider interface {
    // Name returns the provider identifier
    Name() string

    // Init initializes the provider with configuration
    Init(ctx context.Context, config Config) error

    // Close cleans up resources
    Close(ctx context.Context) error

    // Index indexes a single message
    Index(ctx context.Context, msg store.Message) error

    // IndexBatch indexes multiple messages efficiently
    IndexBatch(ctx context.Context, messages []store.Message, batchSize int) error

    // Reindex reindexes messages for an owner (for migration/repair)
    Reindex(ctx context.Context, ownerID string) error

    // Delete removes a message from the index
    Delete(ctx context.Context, messageID string) error

    // Query performs a search
    Query(ctx context.Context, q Query) (Results, error)

    // Health returns provider health status
    Health(ctx context.Context) error
}
```

### 2. Configuration

```go
// Config holds provider-specific configuration
type Config struct {
    // Common settings
    IndexName       string
    Timeout         time.Duration
    MaxRetries      int
    BatchSize       int
    AsyncIndexing   bool

    // Backend-specific
    MongoDBConfig     *MongoDBConfig
    ESConfig          *ElasticsearchConfig
    MeilisearchConfig *MeilisearchConfig
}

type MongoDBConfig struct {
    SearchIndexName string
    Database        string
    Collection      string
}

type ElasticsearchConfig struct {
    Addresses     []string
    Username      string
    Password      string
    CloudID       string
    APIKey        string
    RefreshPolicy string // "true", "false", "wait_for"
}

type MeilisearchConfig struct {
    Host   string
    APIKey string
}
```

### 3. Query Types

```go
// Query represents a unified search request
type Query struct {
    OwnerID         string
    Text            string           // Full-text search query
    Fields          []string         // Fields to search in
    Filters         []Filter         // Faceted filters
    FacetFields     []string         // Fields to aggregate
    Highlight       HighlightSpec
    Sort            []SortSpec
    Pagination      PaginationSpec
}

type Filter struct {
    Field    string
    Operator string // eq, ne, gt, gte, lt, lte, in, exists
    Value    any
}

type HighlightSpec struct {
    Enabled     bool
    Fields      []string
    PreTag      string // default: <em>
    PostTag     string // default: </em>
    MaxLength   int    // max snippet length
}

type SortSpec struct {
    Field string
    Order string // asc, desc
}

type PaginationSpec struct {
    Offset int
    Limit  int
}
```

### 4. Results Types

```go
// Results represents search results
type Results struct {
    Hits            []Hit
    Total           int64
    Took            time.Duration
    Facets          map[string]Facet
}

type Hit struct {
    Message    store.Message
    Score      float64
    Highlights map[string][]string // field -> snippets
}

type Facet struct {
    Field   string
    Buckets []Bucket
}

type Bucket struct {
    Value string
    Count int64
}
```

## Backend-Specific Implementations

### MongoDB Atlas Search

**Features:**
- Native full-text search with MongoDB
- Real-time indexing via change streams
- Rich aggregation pipeline for faceting
- Built-in highlighting
- No separate infrastructure needed

**Implementation Notes:**
```go
type MongoDBProvider struct {
    client     *mongo.Client
    collection *mongo.Collection
    indexName  string
}

// Uses $search aggregation stage
// Query syntax: Atlas Search operators (text, compound, range, etc.)
// Highlighting via "highlight" option
// Faceting via $searchMeta
```

**Go SDK:** `go.mongodb.org/mongo-driver`

### Elasticsearch

**Features:**
- Distributed, scalable search
- Powerful Query DSL
- Rich aggregations
- Real-time indexing with configurable refresh
- Industry standard

**Implementation Notes:**
```go
type ElasticsearchProvider struct {
    client *elasticsearch.Client
    index  string
}

// Uses Query DSL (match, bool, range, etc.)
// Bulk API for efficient indexing
// Configurable refresh policy for latency/throughput tradeoff
// Sharding for multi-tenant scenarios
```

**Go SDK:** `github.com/elastic/go-elasticsearch/v8`

### Meilisearch

**Features:**
- Simple, user-friendly API
- Automatic typo tolerance
- Fast indexing
- Built-in faceting and highlighting
- Easy deployment

**Implementation Notes:**
```go
type MeilisearchProvider struct {
    client *meilisearch.Client
    index  string
}

// Simple REST API
// Task-based indexing (poll for completion)
// Pre-configured searchable attributes
// Ideal for applications prioritizing UX
```

**Go SDK:** `github.com/meilisearch/meilisearch-go`

## Indexing Strategies

### Synchronous Indexing

Best for: MongoDB Atlas Search, Meilisearch, small deployments

```
Message Create -> Index -> Return Response
```

**Pros:**
- Immediate consistency
- Simple implementation
- No queue management

**Cons:**
- Higher write latency
- Blocks message creation

### Asynchronous Indexing

Best for: Elasticsearch, high-throughput systems

```
Message Create -> Enqueue -> Return Response
                     |
                     v (async worker)
              Bulk Index -> Refresh
```

**Pros:**
- Low write latency
- High throughput
- Decoupled operations

**Cons:**
- Eventual consistency
- Requires queue infrastructure
- More complex error handling

```go
// Async indexing support
type IndexQueue interface {
    Enqueue(ctx context.Context, op IndexOperation) error
    Process(ctx context.Context) error
}

type IndexOperation struct {
    Type      string // "index", "update", "delete"
    MessageID string
    Message   store.Message // nil for delete
}
```

## Integration with Mailbox Plugin System

```go
// SearchPlugin wraps a Provider as a mailbox.Plugin
type SearchPlugin struct {
    provider Provider
    config   Config
}

func NewSearchPlugin(provider Provider, config Config) *SearchPlugin {
    return &SearchPlugin{provider: provider, config: config}
}

// Implements mailbox.Plugin
func (sp *SearchPlugin) Name() string {
    return "search-" + sp.provider.Name()
}

func (sp *SearchPlugin) Init(ctx context.Context) error {
    return sp.provider.Init(ctx, sp.config)
}

func (sp *SearchPlugin) Close(ctx context.Context) error {
    return sp.provider.Close(ctx)
}

// Lifecycle hooks for automatic indexing
func (sp *SearchPlugin) AfterCreate(ctx context.Context, userID string, msg store.Message) error {
    return sp.provider.Index(ctx, msg)
}

func (sp *SearchPlugin) AfterUpdate(ctx context.Context, userID string, msg store.Message) error {
    return sp.provider.Index(ctx, msg) // Re-index on update
}

func (sp *SearchPlugin) AfterDelete(ctx context.Context, userID string, messageID string) error {
    return sp.provider.Delete(ctx, messageID)
}

// Search method for mailbox to call
func (sp *SearchPlugin) Search(ctx context.Context, ownerID string, query Query) (Results, error) {
    query.OwnerID = ownerID
    return sp.provider.Query(ctx, query)
}
```

## Field Mapping

```go
// DefaultFieldMapping defines how message fields are indexed
var DefaultFieldMapping = map[string]FieldConfig{
    "subject":       {Type: "text", Searchable: true, Highlightable: true},
    "body":          {Type: "text", Searchable: true, Highlightable: true},
    "sender_id":     {Type: "keyword", Facetable: true},
    "recipient_ids": {Type: "keyword", Facetable: true},
    "folder_id":     {Type: "keyword", Facetable: true},
    "tags":          {Type: "keyword", Facetable: true},
    "status":        {Type: "keyword", Facetable: true},
    "is_read":       {Type: "boolean", Filterable: true},
    "created_at":    {Type: "date", Sortable: true, Filterable: true},
    "updated_at":    {Type: "date", Sortable: true},
    "metadata":      {Type: "object", Dynamic: true},
}

type FieldConfig struct {
    Type          string   // text, keyword, date, boolean, object
    Searchable    bool
    Facetable     bool
    Filterable    bool
    Sortable      bool
    Highlightable bool
    Analyzers     []string // language analyzers
    Dynamic       bool     // for nested objects
}
```

## Usage Example

```go
// Initialize search with MongoDB Atlas Search
atlasProvider := search.NewMongoDBProvider(mongoClient)
searchPlugin := search.NewSearchPlugin(atlasProvider, search.Config{
    IndexName:     "messages_search",
    Timeout:       10 * time.Second,
    AsyncIndexing: false,
    MongoDBConfig: &search.MongoDBConfig{
        SearchIndexName: "default",
        Database:        "mailbox",
        Collection:      "messages",
    },
})

// Create mailbox service with search plugin
svc, _ := mailbox.NewService(
    mailbox.WithStore(mongoStore),
    mailbox.WithPlugin(searchPlugin),
)

// Search messages
client := svc.Client("user123")
results, _ := searchPlugin.Search(ctx, "user123", search.Query{
    Text:        "meeting agenda",
    Fields:      []string{"subject", "body"},
    FacetFields: []string{"tags", "folder_id"},
    Highlight:   search.HighlightSpec{Enabled: true},
    Pagination:  search.PaginationSpec{Limit: 20},
})
```

## Tradeoffs Summary

| Aspect | Sync Indexing | Async Indexing |
|--------|--------------|----------------|
| Latency | High write latency | Low write latency |
| Consistency | Immediate | Eventual |
| Complexity | Simple | Complex (queue mgmt) |
| Scale | Medium | Large |
| Failure handling | Transactional | Retry logic needed |
| Best for | MongoDB, Meilisearch | Elasticsearch |

## Implementation Priority

1. **Phase 1 (MVP):** MongoDB Atlas Search (leverages existing MongoDB client)
2. **Phase 2:** Meilisearch (simple implementation, good developer UX)
3. **Phase 3:** Elasticsearch (highest complexity, most features)
4. **Phase 4:** Custom provider interface documentation for user implementations

## Package Structure

```
mailbox/
├── search/
│   ├── search.go           # Provider interface, types
│   ├── plugin.go           # SearchPlugin implementation
│   ├── mongo/
│   │   └── provider.go     # MongoDB Atlas Search
│   ├── elasticsearch/
│   │   └── provider.go     # Elasticsearch
│   └── meilisearch/
│       └── provider.go     # Meilisearch
```
