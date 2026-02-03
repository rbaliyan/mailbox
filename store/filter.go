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

// Filter represents a query filter.
type Filter interface {
	Key() string
	Value() any
	Operator() string
}

// Filters is a filter builder for a specific field.
type Filters interface {
	Equal(v any) (Filter, error)
	NotEqual(v any) (Filter, error)
	GreaterThan(v any) (Filter, error)
	GreaterThanEqual(v any) (Filter, error)
	LessThan(v any) (Filter, error)
	LessThanEqual(v any) (Filter, error)
	In(v ...any) (Filter, error)
	NotIn(v ...any) (Filter, error)
	Exists(v bool) (Filter, error)
	Contains(v any) (Filter, error)
}

// filter implements Filter.
type filter struct {
	key      string
	value    any
	operator string
}

func (f *filter) Key() string      { return f.key }
func (f *filter) Value() any       { return f.value }
func (f *filter) Operator() string { return f.operator }

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
		return nil, fmt.Errorf("%w: unsupported field: %s", ErrFilterInvalid, key)
	}
	if !validOperators[operator] {
		return nil, fmt.Errorf("%w: unsupported operator: %s", ErrFilterInvalid, operator)
	}
	return &filter{key: storageKey, value: value, operator: operator}, nil
}

// filterBuilder implements Filters.
type filterBuilder struct {
	key string
	err error
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

func (b *filterBuilder) build(op string, v any) (Filter, error) {
	if b.err != nil {
		return nil, &FilterError{Key: b.key, Err: b.err}
	}
	return &filter{key: b.key, value: v, operator: op}, nil
}

func (b *filterBuilder) Equal(v any) (Filter, error)            { return b.build("eq", v) }
func (b *filterBuilder) NotEqual(v any) (Filter, error)         { return b.build("ne", v) }
func (b *filterBuilder) GreaterThan(v any) (Filter, error)      { return b.build("gt", v) }
func (b *filterBuilder) GreaterThanEqual(v any) (Filter, error) { return b.build("gte", v) }
func (b *filterBuilder) LessThan(v any) (Filter, error)         { return b.build("lt", v) }
func (b *filterBuilder) LessThanEqual(v any) (Filter, error)    { return b.build("lte", v) }
func (b *filterBuilder) In(v ...any) (Filter, error)            { return b.build("in", v) }
func (b *filterBuilder) NotIn(v ...any) (Filter, error)         { return b.build("nin", v) }
func (b *filterBuilder) Exists(v bool) (Filter, error)          { return b.build("exists", v) }
func (b *filterBuilder) Contains(v any) (Filter, error)         { return b.build("contains", v) }

// MessageFilter returns a filter builder for message fields.
func MessageFilter(field string) Filters {
	key, ok := MessageFieldKey(field)
	if !ok {
		return &filterBuilder{key: field, err: fmt.Errorf("unsupported field: %s", field)}
	}
	return &filterBuilder{key: key}
}

// MessageFieldKey maps field names to storage keys.
func MessageFieldKey(field string) (string, bool) {
	switch field {
	case "ID", "id":
		return "_id", true
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
	case "Deleted", "deleted", "__deleted":
		return "__deleted", true
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

// NotDeleted returns a filter for non-deleted messages.
func NotDeleted() Filter {
	f, _ := MessageFilter("Deleted").Equal(false)
	return f
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

