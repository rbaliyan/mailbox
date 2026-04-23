package elasticsearch

// options holds Elasticsearch provider configuration.
type options struct {
	addresses []string
	username  string
	password  string
	apiKey    string
	cloudID   string
	index     string
}

// Option configures the Elasticsearch provider.
type Option func(*options)

// WithAddresses sets the list of Elasticsearch node addresses.
// Defaults to ["http://localhost:9200"] when neither addresses nor cloudID is set.
func WithAddresses(addrs []string) Option {
	return func(o *options) {
		if len(addrs) > 0 {
			o.addresses = addrs
		}
	}
}

// WithUsername sets the username for HTTP Basic Authentication.
func WithUsername(username string) Option {
	return func(o *options) {
		o.username = username
	}
}

// WithPassword sets the password for HTTP Basic Authentication.
func WithPassword(password string) Option {
	return func(o *options) {
		o.password = password
	}
}

// WithAPIKey sets the base64-encoded API key for authorization.
// When set, it overrides username and password.
func WithAPIKey(key string) Option {
	return func(o *options) {
		o.apiKey = key
	}
}

// WithCloudID sets the Elastic Cloud ID for managed Elasticsearch.
// When set, it overrides Addresses.
func WithCloudID(cloudID string) Option {
	return func(o *options) {
		if cloudID != "" {
			o.cloudID = cloudID
		}
	}
}

// WithIndex sets the Elasticsearch index name. Defaults to "mailbox_messages".
func WithIndex(index string) Option {
	return func(o *options) {
		if index != "" {
			o.index = index
		}
	}
}
