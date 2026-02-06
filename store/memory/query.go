package memory

import (
	"context"
	"strings"

	"github.com/rbaliyan/mailbox/store"
)

// Get retrieves a message by ID.
func (s *Store) Get(ctx context.Context, id string) (store.Message, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, store.ErrInvalidID
	}

	v, ok := s.messages.Load(id)
	if !ok {
		return nil, store.ErrNotFound
	}

	m := v.(*message)
	if m.isDraft {
		return nil, store.ErrNotFound // drafts are not messages
	}

	return m.clone(), nil
}

// Find retrieves messages matching the filters.
func (s *Store) Find(ctx context.Context, filters []store.Filter, opts store.ListOptions) (*store.MessageList, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	var all []*message
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if !m.isDraft && matchesFilters(m, filters) {
			all = append(all, m)
		}
		return true
	})

	// Sort
	sortBy := opts.SortBy
	if sortBy == "" {
		sortBy = "created_at"
	}
	sortMessages(all, sortBy, opts.SortOrder)

	total := int64(len(all))

	// Apply cursor-based pagination using StartAfter
	start := 0
	if opts.StartAfter != "" {
		found := false
		for i, m := range all {
			if m.id == opts.StartAfter {
				start = i + 1 // Start after this message
				found = true
				break
			}
		}
		if !found {
			// Cursor not found (deleted). Return empty results since the page
			// boundary is unknown. Callers should re-query without a cursor.
			return &store.MessageList{Total: total}, nil
		}
	}

	end := start + opts.Limit
	if opts.Limit == 0 {
		end = len(all)
	}
	if end > len(all) {
		end = len(all)
	}

	result := all[start:end]
	messages := make([]store.Message, len(result))
	for i, m := range result {
		messages[i] = m.clone()
	}

	return &store.MessageList{
		Messages: messages,
		Total:    total,
		HasMore:  end < len(all),
	}, nil
}

// Count returns the count of messages matching the filters.
func (s *Store) Count(ctx context.Context, filters []store.Filter) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}

	var count int64
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if !m.isDraft && matchesFilters(m, filters) {
			count++
		}
		return true
	})
	return count, nil
}

// Search performs full-text search on messages.
func (s *Store) Search(ctx context.Context, query store.SearchQuery) (*store.MessageList, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	var all []*message
	searchTerm := strings.ToLower(query.Query)

	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.isDraft {
			return true
		}
		if query.OwnerID != "" && m.ownerID != query.OwnerID {
			return true
		}
		if !matchesFilters(m, query.Filters) {
			return true
		}
		if len(query.Tags) > 0 && !hasAllTags(m, query.Tags) {
			return true
		}
		if searchTerm != "" {
			if !strings.Contains(strings.ToLower(m.subject), searchTerm) &&
				!strings.Contains(strings.ToLower(m.body), searchTerm) {
				return true
			}
		}
		all = append(all, m)
		return true
	})

	// Sort and paginate
	sortMessages(all, query.Options.SortBy, query.Options.SortOrder)

	total := int64(len(all))

	// Apply cursor-based pagination using StartAfter
	start := 0
	if query.Options.StartAfter != "" {
		found := false
		for i, m := range all {
			if m.id == query.Options.StartAfter {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return &store.MessageList{Total: total}, nil
		}
	}

	end := start + query.Options.Limit
	if query.Options.Limit == 0 {
		end = len(all)
	}
	if end > len(all) {
		end = len(all)
	}

	result := all[start:end]
	messages := make([]store.Message, len(result))
	for i, m := range result {
		messages[i] = m.clone()
	}

	return &store.MessageList{
		Messages: messages,
		Total:    total,
		HasMore:  end < len(all),
	}, nil
}
