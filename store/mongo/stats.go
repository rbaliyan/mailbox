package mongo

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/rbaliyan/mailbox/store"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// =============================================================================
// Folder Operations (FolderCounter, FindWithCounter, FolderLister)
// =============================================================================

// CountByFolders returns message counts and unread counts for the given folders.
// Uses MongoDB $match + $group aggregation for efficient batch counting.
func (s *Store) CountByFolders(ctx context.Context, ownerID string, folderIDs []string) (map[string]store.FolderCounts, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	pipeline := bson.A{
		bson.M{"$match": bson.M{
			"owner_id":   ownerID,
			"__is_draft": bson.M{"$ne": true},
			"folder_id":  bson.M{"$in": folderIDs},
		}},
		bson.M{"$group": bson.M{
			"_id":    "$folder_id",
			"total":  bson.M{"$sum": 1},
			"unread": bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$ne": bson.A{"$is_read", true}}, 1, 0}}},
		}},
	}

	cursor, err := s.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("count by folders: %w", err)
	}
	defer cursor.Close(ctx)

	var results []struct {
		FolderID string `bson:"_id"`
		Total    int64  `bson:"total"`
		Unread   int64  `bson:"unread"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode folder counts: %w", err)
	}

	counts := make(map[string]store.FolderCounts, len(results))
	for _, r := range results {
		counts[r.FolderID] = store.FolderCounts{Total: r.Total, Unread: r.Unread}
	}
	return counts, nil
}

// FindWithCount retrieves messages and total count in a single operation.
// Delegates to Find + Count for simplicity; MongoDB doesn't have a single-query equivalent.
func (s *Store) FindWithCount(ctx context.Context, filters []store.Filter, opts store.ListOptions) (*store.MessageList, int64, error) {
	list, err := s.Find(ctx, filters, opts)
	if err != nil {
		return nil, 0, err
	}
	count, err := s.Count(ctx, filters)
	if err != nil {
		return nil, 0, err
	}
	return list, count, nil
}

// ListDistinctFolders returns all distinct folder IDs for a user's non-deleted messages.
func (s *Store) ListDistinctFolders(ctx context.Context, ownerID string) ([]string, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	filter := bson.M{
		"owner_id":   ownerID,
		"__is_draft": bson.M{"$ne": true},
	}

	var folders []string
	if err := s.collection.Distinct(ctx, "folder_id", filter).Decode(&folders); err != nil {
		return nil, fmt.Errorf("list distinct folders: %w", err)
	}

	return folders, nil
}

// =============================================================================
// Stats Operations
// =============================================================================

// MailboxStats returns aggregate statistics for a user's mailbox using a single
// MongoDB $facet aggregation pipeline.
func (s *Store) MailboxStats(ctx context.Context, ownerID string) (*store.MailboxStats, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	pipeline := bson.A{
		bson.M{"$match": bson.M{"owner_id": ownerID}},
		bson.M{"$facet": bson.M{
			"messages": bson.A{
				bson.M{"$match": bson.M{"__is_draft": bson.M{"$ne": true}}},
				bson.M{"$group": bson.M{
					"_id":    "$folder_id",
					"total":  bson.M{"$sum": 1},
					"unread": bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$ne": bson.A{"$is_read", true}}, 1, 0}}},
				}},
			},
			"drafts": bson.A{
				bson.M{"$match": bson.M{"__is_draft": true}},
				bson.M{"$count": "n"},
			},
		}},
	}

	cursor, err := s.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate mailbox stats: %w", err)
	}
	defer cursor.Close(ctx)

	var results []struct {
		Messages []struct {
			FolderID string `bson:"_id"`
			Total    int64  `bson:"total"`
			Unread   int64  `bson:"unread"`
		} `bson:"messages"`
		Drafts []struct {
			N int64 `bson:"n"`
		} `bson:"drafts"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode mailbox stats: %w", err)
	}

	stats := &store.MailboxStats{
		Folders: make(map[string]store.FolderCounts),
	}

	if len(results) > 0 {
		r := results[0]
		for _, m := range r.Messages {
			stats.TotalMessages += m.Total
			stats.UnreadCount += m.Unread
			stats.Folders[m.FolderID] = store.FolderCounts{
				Total:  m.Total,
				Unread: m.Unread,
			}
		}
		if len(r.Drafts) > 0 {
			stats.DraftCount = r.Drafts[0].N
		}
	}

	return stats, nil
}
