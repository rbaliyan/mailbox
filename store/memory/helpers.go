package memory

import (
	"strings"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

func matchesFilters(m *message, filters []store.Filter) bool {
	for _, f := range filters {
		if !matchesFilter(m, f) {
			return false
		}
	}
	return true
}

func matchesFilter(m *message, f store.Filter) bool {
	key := f.Key()
	value := f.Value()
	op := f.Operator()

	// Handle slice-based fields with special operators.
	switch key {
	case "tags":
		return matchesSliceFilter(m.tags, op, value)
	case "recipient_ids":
		return matchesSliceFilter(m.recipientIDs, op, value)
	}

	// Scalar fields.
	var fieldValue any
	switch key {
	case "id":
		fieldValue = m.id
	case "owner_id":
		fieldValue = m.ownerID
	case "sender_id":
		fieldValue = m.senderID
	case "subject":
		fieldValue = m.subject
	case "folder_id":
		fieldValue = m.folderID
	case "is_read":
		fieldValue = m.isRead
	case "status":
		fieldValue = m.status
	case "thread_id":
		fieldValue = m.threadID
	case "reply_to_id":
		fieldValue = m.replyToID
	case "created_at":
		fieldValue = m.createdAt
	case "updated_at":
		fieldValue = m.updatedAt
	default:
		return true // Unknown field, skip filter
	}

	switch op {
	case "eq", "=", "":
		return fieldValue == value
	case "ne", "!=":
		return fieldValue != value
	case "lt", "<":
		return compareValues(fieldValue, value) < 0
	case "lte", "<=":
		return compareValues(fieldValue, value) <= 0
	case "gt", ">":
		return compareValues(fieldValue, value) > 0
	case "gte", ">=":
		return compareValues(fieldValue, value) >= 0
	case "exists":
		exists, _ := value.(bool)
		isEmpty := fieldValue == "" || fieldValue == nil
		return exists != isEmpty
	case "in":
		return valueInSet(fieldValue, value)
	case "nin":
		return !valueInSet(fieldValue, value)
	default:
		return true
	}
}

// matchesSliceFilter handles filter operations on slice fields (tags, recipient_ids).
func matchesSliceFilter(slice []string, op string, value any) bool {
	switch op {
	case "contains":
		s, ok := value.(string)
		if !ok {
			return false
		}
		for _, item := range slice {
			if item == s {
				return true
			}
		}
		return false
	case "exists":
		exists, _ := value.(bool)
		hasItems := len(slice) > 0
		return exists == hasItems
	case "eq", "=", "":
		// Equality on slice: check if all elements match
		other, ok := value.([]string)
		if !ok {
			return false
		}
		if len(slice) != len(other) {
			return false
		}
		for i := range slice {
			if slice[i] != other[i] {
				return false
			}
		}
		return true
	default:
		return true
	}
}

// valueInSet checks if a scalar value is in a set (slice) of values.
func valueInSet(fieldValue any, set any) bool {
	switch s := set.(type) {
	case []string:
		fv, ok := fieldValue.(string)
		if !ok {
			return false
		}
		for _, v := range s {
			if v == fv {
				return true
			}
		}
	case []any:
		for _, v := range s {
			if v == fieldValue {
				return true
			}
		}
	}
	return false
}

func compareValues(a, b any) int {
	switch av := a.(type) {
	case string:
		if bv, ok := b.(string); ok {
			return strings.Compare(av, bv)
		}
	case int:
		if bv, ok := b.(int); ok {
			if av < bv {
				return -1
			} else if av > bv {
				return 1
			}
			return 0
		}
	case int64:
		if bv, ok := b.(int64); ok {
			if av < bv {
				return -1
			} else if av > bv {
				return 1
			}
			return 0
		}
	case time.Time:
		if bv, ok := b.(time.Time); ok {
			if av.Before(bv) {
				return -1
			} else if av.After(bv) {
				return 1
			}
			return 0
		}
	}
	return 0
}

func hasAllTags(m *message, tags []string) bool {
	tagSet := make(map[string]bool, len(m.tags))
	for _, t := range m.tags {
		tagSet[t] = true
	}
	for _, t := range tags {
		if !tagSet[t] {
			return false
		}
	}
	return true
}

func sortMessages(msgs []*message, sortBy string, order store.SortOrder) {
	if sortBy == "" {
		sortBy = "created_at"
	}
	if order == 0 {
		order = store.SortDesc
	}

	// Simple bubble sort for testing
	for i := 0; i < len(msgs)-1; i++ {
		for j := i + 1; j < len(msgs); j++ {
			shouldSwap := false
			switch sortBy {
			case "created_at":
				if order == store.SortAsc {
					shouldSwap = msgs[i].createdAt.After(msgs[j].createdAt)
				} else {
					shouldSwap = msgs[i].createdAt.Before(msgs[j].createdAt)
				}
			case "updated_at":
				if order == store.SortAsc {
					shouldSwap = msgs[i].updatedAt.After(msgs[j].updatedAt)
				} else {
					shouldSwap = msgs[i].updatedAt.Before(msgs[j].updatedAt)
				}
			case "subject":
				if order == store.SortAsc {
					shouldSwap = msgs[i].subject > msgs[j].subject
				} else {
					shouldSwap = msgs[i].subject < msgs[j].subject
				}
			}
			if shouldSwap {
				msgs[i], msgs[j] = msgs[j], msgs[i]
			}
		}
	}
}
