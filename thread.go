package mailbox

import (
	"context"
	"fmt"

	"github.com/rbaliyan/mailbox/store"
)

// ThreadParticipants returns the distinct user IDs of all non-deleted participants
// in the given thread. Delegates to the store's ThreadParticipantLister when available
// (single-query path). Returns store.ErrNotFound when no messages exist for the thread_id.
func (s *service) ThreadParticipants(ctx context.Context, threadID string) ([]string, error) {
	if !s.IsConnected() {
		return nil, ErrNotConnected
	}
	if threadID == "" {
		return nil, fmt.Errorf("%w: thread_id is required", ErrInvalidMessage)
	}

	if lister, ok := s.store.(store.ThreadParticipantLister); ok {
		return lister.ThreadParticipants(ctx, threadID)
	}

	// Fallback: paginated scan via Find. Only reached for custom store implementations
	// that do not implement ThreadParticipantLister.
	seen := make(map[string]struct{})
	opts := store.ListOptions{Limit: 100}
	for {
		list, err := s.store.Find(ctx, []store.Filter{
			store.ThreadIs(threadID),
			store.NotInFolder(store.FolderTrash),
		}, opts)
		if err != nil {
			return nil, fmt.Errorf("thread participants scan: %w", err)
		}
		for _, msg := range list.Messages {
			seen[msg.GetOwnerID()] = struct{}{}
		}
		if !list.HasMore {
			break
		}
		opts.StartAfter = list.NextCursor
	}

	if len(seen) == 0 {
		return nil, store.ErrNotFound
	}

	participants := make([]string, 0, len(seen))
	for ownerID := range seen {
		participants = append(participants, ownerID)
	}
	return participants, nil
}
