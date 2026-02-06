package store

import (
	"fmt"
)

// SortOrder represents the sort direction.
type SortOrder int

const (
	// SortAsc sorts in ascending order.
	SortAsc SortOrder = 1
	// SortDesc sorts in descending order.
	SortDesc SortOrder = -1
)

// ListOptions configures message listing.
type ListOptions struct {
	Limit      int
	Offset     int
	SortBy     string
	SortOrder  SortOrder
	StartAfter string // cursor-based pagination
}

// SearchQuery represents a search request.
type SearchQuery struct {
	OwnerID string      // required: owner of the messages
	Query   string      // text search query
	Fields  []string    // fields to search in (subject, body)
	Tags    []string    // filter by tags (messages must have all specified tags)
	Filters []Filter    // additional filters
	Options ListOptions // pagination and sorting
}

// Filter represents a query filter with a field key, comparison operator, and value.
type Filter struct {
	key      string
	value    any
	operator string
}

// Key returns the storage field key.
func (f Filter) Key() string { return f.key }

// Value returns the filter value.
func (f Filter) Value() any { return f.value }

// Operator returns the comparison operator (eq, ne, gt, gte, lt, lte, in, nin, exists, contains).
func (f Filter) Operator() string { return f.operator }

// FilterBuilder builds filters for a specific message field.
// Use MessageFilter() to create one, then chain a comparison method:
//
//	filter, err := store.MessageFilter("CreatedAt").GreaterThan(cutoff)
type FilterBuilder struct {
	key string
	err error
}

// validOperators is the set of supported filter operators.
var validOperators = map[string]bool{
	"eq":       true,
	"ne":       true,
	"gt":       true,
	"gte":      true,
	"lt":       true,
	"lte":      true,
	"in":       true,
	"nin":      true,
	"exists":   true,
	"contains": true,
}

// NewFilter creates a filter with the given key, operator, and value.
// The key must be a valid message field (validated via MessageFieldKey).
// The operator must be one of: eq, ne, gt, gte, lt, lte, in, nin, exists, contains.
// Returns ErrFilterInvalid if the key or operator is invalid.
func NewFilter(key, operator string, value any) (Filter, error) {
	storageKey, ok := MessageFieldKey(key)
	if !ok {
		return Filter{}, fmt.Errorf("%w: unsupported field: %s", ErrFilterInvalid, key)
	}
	if !validOperators[operator] {
		return Filter{}, fmt.Errorf("%w: unsupported operator: %s", ErrFilterInvalid, operator)
	}
	return Filter{key: storageKey, value: value, operator: operator}, nil
}

// FilterError represents an error in filter building.
type FilterError struct {
	Key string
	Err error
}

func (e *FilterError) Error() string {
	return fmt.Sprintf("filter %s: %v", e.Key, e.Err)
}

func (e *FilterError) Unwrap() error {
	return e.Err
}

func (b *FilterBuilder) build(op string, v any) (Filter, error) {
	if b.err != nil {
		return Filter{}, &FilterError{Key: b.key, Err: b.err}
	}
	return Filter{key: b.key, value: v, operator: op}, nil
}

func (b *FilterBuilder) Equal(v any) (Filter, error)            { return b.build("eq", v) }
func (b *FilterBuilder) NotEqual(v any) (Filter, error)         { return b.build("ne", v) }
func (b *FilterBuilder) GreaterThan(v any) (Filter, error)      { return b.build("gt", v) }
func (b *FilterBuilder) GreaterThanEqual(v any) (Filter, error) { return b.build("gte", v) }
func (b *FilterBuilder) LessThan(v any) (Filter, error)         { return b.build("lt", v) }
func (b *FilterBuilder) LessThanEqual(v any) (Filter, error)    { return b.build("lte", v) }
func (b *FilterBuilder) In(v ...any) (Filter, error)            { return b.build("in", v) }
func (b *FilterBuilder) NotIn(v ...any) (Filter, error)         { return b.build("nin", v) }
func (b *FilterBuilder) Exists(v bool) (Filter, error)          { return b.build("exists", v) }
func (b *FilterBuilder) Contains(v any) (Filter, error)         { return b.build("contains", v) }

// MessageFilter returns a filter builder for message fields.
func MessageFilter(field string) *FilterBuilder {
	key, ok := MessageFieldKey(field)
	if !ok {
		return &FilterBuilder{key: field, err: fmt.Errorf("unsupported field: %s", field)}
	}
	return &FilterBuilder{key: key}
}

// MessageFieldKey maps field names to storage keys.
func MessageFieldKey(field string) (string, bool) {
	switch field {
	case "ID", "id":
		return "id", true
	case "OwnerID", "owner_id":
		return "owner_id", true
	case "SenderID", "sender_id":
		return "sender_id", true
	case "RecipientIDs", "recipient_ids":
		return "recipient_ids", true
	case "Subject", "subject":
		return "subject", true
	case "Body", "body":
		return "body", true
	case "Status", "status":
		return "status", true
	case "IsRead", "is_read":
		return "is_read", true
	case "FolderID", "folder_id":
		return "folder_id", true
	case "Tags", "tags":
		return "tags", true
	case "CreatedAt", "created_at":
		return "created_at", true
	case "UpdatedAt", "updated_at":
		return "updated_at", true
	case "ThreadID", "thread_id":
		return "thread_id", true
	case "ReplyToID", "reply_to_id":
		return "reply_to_id", true
	default:
		return "", false
	}
}

// MessageOrderingKey returns the storage key for sorting.
func MessageOrderingKey(field string) (string, bool) {
	return MessageFieldKey(field)
}

// Convenience filter functions

// OwnerIs returns a filter for messages owned by a specific user.
func OwnerIs(ownerID string) Filter {
	f, _ := MessageFilter("OwnerID").Equal(ownerID)
	return f
}

// SenderIs returns a filter for messages from a specific sender.
func SenderIs(senderID string) Filter {
	f, _ := MessageFilter("SenderID").Equal(senderID)
	return f
}

// RecipientIs returns a filter for messages to a specific recipient.
func RecipientIs(recipientID string) Filter {
	f, _ := MessageFilter("RecipientIDs").Contains(recipientID)
	return f
}

// StatusIs returns a filter for messages with a specific status.
func StatusIs(status MessageStatus) Filter {
	f, _ := MessageFilter("Status").Equal(string(status))
	return f
}

// NotDeleted returns a filter that excludes messages in the trash folder.
func NotDeleted() Filter {
	return NotInFolder(FolderTrash)
}

// IsReadFilter returns a filter for read/unread messages.
func IsReadFilter(isRead bool) Filter {
	f, _ := MessageFilter("IsRead").Equal(isRead)
	return f
}

// InFolder returns a filter for messages in a specific folder.
func InFolder(folderID string) Filter {
	f, _ := MessageFilter("FolderID").Equal(folderID)
	return f
}

// NotInFolder returns a filter for messages not in a specific folder.
func NotInFolder(folderID string) Filter {
	f, _ := MessageFilter("FolderID").NotEqual(folderID)
	return f
}

// HasTag returns a filter for messages with a specific tag.
func HasTag(tagID string) Filter {
	f, _ := MessageFilter("Tags").Contains(tagID)
	return f
}

// HasAnyTag returns a filter for messages that have any tags.
func HasAnyTag() Filter {
	f, _ := MessageFilter("Tags").Exists(true)
	return f
}

// HasTags returns filters for messages that have all specified tags.
func HasTags(tags []string) []Filter {
	filters := make([]Filter, len(tags))
	for i, tag := range tags {
		filters[i] = HasTag(tag)
	}
	return filters
}

// ThreadIs returns a filter for messages in a specific thread.
func ThreadIs(threadID string) Filter {
	f, _ := MessageFilter("ThreadID").Equal(threadID)
	return f
}

// ReplyToIs returns a filter for messages that are replies to a specific message.
func ReplyToIs(messageID string) Filter {
	f, _ := MessageFilter("ReplyToID").Equal(messageID)
	return f
}

// HasThread returns a filter for messages that belong to any thread.
func HasThread() Filter {
	f, _ := MessageFilter("ThreadID").Exists(true)
	return f
}

