package meilisearch

// options holds Meilisearch provider configuration.
type options struct {
	host              string
	apiKey            string
	index             string
	searchableFields  []string
	filterableFields  []string
}

// Option configures the Meilisearch provider.
type Option func(*options)

// WithHost sets the Meilisearch server URL. Defaults to "http://localhost:7700".
func WithHost(host string) Option {
	return func(o *options) {
		if host != "" {
			o.host = host
		}
	}
}

// WithAPIKey sets the Meilisearch API key.
func WithAPIKey(key string) Option {
	return func(o *options) {
		o.apiKey = key
	}
}

// WithIndex sets the Meilisearch index UID. Defaults to "mailbox_messages".
func WithIndex(index string) Option {
	return func(o *options) {
		if index != "" {
			o.index = index
		}
	}
}

// WithSearchableFields sets the fields Meilisearch will index for full-text search.
// Defaults to ["subject", "body"].
func WithSearchableFields(fields []string) Option {
	return func(o *options) {
		if len(fields) > 0 {
			o.searchableFields = fields
		}
	}
}

// WithFilterableFields sets the fields Meilisearch will make available for filtering.
// Defaults to ["owner_id", "folder_id", "tags", "is_read"].
func WithFilterableFields(fields []string) Option {
	return func(o *options) {
		if len(fields) > 0 {
			o.filterableFields = fields
		}
	}
}
