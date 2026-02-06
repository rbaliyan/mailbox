package mongo

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MarkRead sets the read status of a message.
func (s *Store) MarkRead(ctx context.Context, id string, read bool) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return store.ErrInvalidID
	}

	now := time.Now().UTC()
	update := bson.M{
		"$set": bson.M{
			"is_read":    read,
			"updated_at": now,
		},
	}
	if read {
		update["$set"].(bson.M)["read_at"] = now
	} else {
		update["$unset"] = bson.M{"read_at": ""}
	}

	filter := bson.M{
		"_id":        oid,
		"__is_draft": bson.M{"$ne": true},
	}

	result, err := s.collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}

	if result.MatchedCount == 0 {
		return store.ErrNotFound
	}

	return nil
}

// MoveToFolder moves a message to a different folder.
func (s *Store) MoveToFolder(ctx context.Context, id string, folderID string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}

	if !store.IsValidFolderID(folderID) {
		return store.ErrInvalidFolderID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return store.ErrInvalidID
	}

	filter := bson.M{
		"_id":        oid,
		"__is_draft": bson.M{"$ne": true},
	}
	update := bson.M{
		"$set": bson.M{
			"folder_id":  folderID,
			"updated_at": time.Now().UTC(),
		},
	}

	result, err := s.collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("move to folder: %w", err)
	}

	if result.MatchedCount == 0 {
		return store.ErrNotFound
	}

	return nil
}

// AddTag adds a tag to a message.
func (s *Store) AddTag(ctx context.Context, id string, tagID string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return store.ErrInvalidID
	}

	filter := bson.M{
		"_id":        oid,
		"__is_draft": bson.M{"$ne": true},
	}
	update := bson.M{
		"$addToSet": bson.M{"tags": tagID},
		"$set":      bson.M{"updated_at": time.Now().UTC()},
	}

	result, err := s.collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("add tag: %w", err)
	}

	if result.MatchedCount == 0 {
		return store.ErrNotFound
	}

	return nil
}

// RemoveTag removes a tag from a message.
func (s *Store) RemoveTag(ctx context.Context, id string, tagID string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return store.ErrInvalidID
	}

	filter := bson.M{
		"_id":        oid,
		"__is_draft": bson.M{"$ne": true},
	}
	update := bson.M{
		"$pull": bson.M{"tags": tagID},
		"$set":  bson.M{"updated_at": time.Now().UTC()},
	}

	result, err := s.collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}

	if result.MatchedCount == 0 {
		return store.ErrNotFound
	}

	return nil
}

// Delete soft-deletes a message.
func (s *Store) Delete(ctx context.Context, id string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return store.ErrInvalidID
	}

	filter := bson.M{
		"_id":        oid,
		"__is_draft": bson.M{"$ne": true},
		"folder_id":  bson.M{"$ne": store.FolderTrash},
	}
	update := bson.M{
		"$set": bson.M{
			"folder_id":  store.FolderTrash,
			"updated_at": time.Now().UTC(),
		},
	}

	result, err := s.collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("move to trash: %w", err)
	}

	if result.MatchedCount == 0 {
		return store.ErrNotFound
	}

	return nil
}

// HardDelete permanently removes a message.
func (s *Store) HardDelete(ctx context.Context, id string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return store.ErrInvalidID
	}

	filter := bson.M{
		"_id":        oid,
		"__is_draft": bson.M{"$ne": true},
	}

	result, err := s.collection.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("hard delete message: %w", err)
	}

	if result.DeletedCount == 0 {
		return store.ErrNotFound
	}

	return nil
}

// Restore restores a soft-deleted message.
// Determines the correct folder based on ownership (sent vs inbox).
func (s *Store) Restore(ctx context.Context, id string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return store.ErrInvalidID
	}

	// Read the message to determine the correct restore folder
	var doc messageDoc
	err = s.collection.FindOne(ctx, bson.M{
		"_id":        oid,
		"__is_draft": bson.M{"$ne": true},
		"folder_id":  store.FolderTrash,
	}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return store.ErrNotFound
		}
		return fmt.Errorf("find message for restore: %w", err)
	}

	// Determine restore folder based on ownership
	folderID := store.FolderInbox
	if store.IsSentByOwner(doc.OwnerID, doc.SenderID) {
		folderID = store.FolderSent
	}

	filter := bson.M{
		"_id":       oid,
		"folder_id": store.FolderTrash,
	}
	update := bson.M{
		"$set": bson.M{
			"folder_id":  folderID,
			"updated_at": time.Now().UTC(),
		},
	}

	result, err := s.collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("restore message: %w", err)
	}

	if result.MatchedCount == 0 {
		return store.ErrNotFound
	}

	return nil
}

// CreateMessage creates a new message from the given data.
func (s *Store) CreateMessage(ctx context.Context, data store.MessageData) (store.Message, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()
	doc := &messageDoc{
		OwnerID:      data.OwnerID,
		SenderID:     data.SenderID,
		RecipientIDs: data.RecipientIDs,
		Subject:      data.Subject,
		Body:         data.Body,
		Metadata:     data.Metadata,
		Status:       string(data.Status),
		FolderID:     data.FolderID,
		CreatedAt:    now,
		UpdatedAt:    now,
		IsDraft:      false,
		ThreadID:     data.ThreadID,
		ReplyToID:    data.ReplyToID,
	}

	if len(data.Tags) > 0 {
		doc.Tags = data.Tags
	}

	if len(data.Attachments) > 0 {
		doc.Attachments = make([]attachmentDoc, len(data.Attachments))
		for i, a := range data.Attachments {
			doc.Attachments[i] = attachmentDoc{
				ID:          a.GetID(),
				Filename:    a.GetFilename(),
				ContentType: a.GetContentType(),
				Size:        a.GetSize(),
				URI:         a.GetURI(),
				CreatedAt:   a.GetCreatedAt(),
			}
		}
	}

	result, err := s.collection.InsertOne(ctx, doc)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, store.ErrDuplicateEntry
		}
		return nil, fmt.Errorf("insert message: %w", err)
	}

	if oid, ok := result.InsertedID.(bson.ObjectID); ok {
		doc.ID = oid
	}

	return docToMessage(doc), nil
}

// CreateMessages creates multiple messages in a batch using a transaction for atomicity.
// If the MongoDB deployment does not support transactions (e.g., standalone),
// falls back to a non-transactional InsertMany.
func (s *Store) CreateMessages(ctx context.Context, data []store.MessageData) ([]store.Message, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	if len(data) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()
	docs := make([]any, len(data))
	docRefs := make([]*messageDoc, len(data))

	for i, d := range data {
		doc := &messageDoc{
			OwnerID:      d.OwnerID,
			SenderID:     d.SenderID,
			RecipientIDs: d.RecipientIDs,
			Subject:      d.Subject,
			Body:         d.Body,
			Metadata:     d.Metadata,
			Status:       string(d.Status),
			FolderID:     d.FolderID,
			Tags:         d.Tags,
			CreatedAt:    now,
			UpdatedAt:    now,
			IsDraft:      false,
			ThreadID:     d.ThreadID,
			ReplyToID:    d.ReplyToID,
		}

		if len(d.Attachments) > 0 {
			doc.Attachments = make([]attachmentDoc, len(d.Attachments))
			for j, a := range d.Attachments {
				doc.Attachments[j] = attachmentDoc{
					ID:          a.GetID(),
					Filename:    a.GetFilename(),
					ContentType: a.GetContentType(),
					Size:        a.GetSize(),
					URI:         a.GetURI(),
					CreatedAt:   a.GetCreatedAt(),
				}
			}
		}

		docs[i] = doc
		docRefs[i] = doc
	}

	// Try transactional insert first for atomicity
	session, err := s.client.StartSession()
	if err != nil {
		// Standalone MongoDB doesn't support sessions - fall back to plain InsertMany
		return s.insertManyFallback(ctx, docs, docRefs)
	}
	defer session.EndSession(ctx)

	var messages []store.Message
	_, txErr := session.WithTransaction(ctx, func(sessCtx context.Context) (any, error) {
		result, insertErr := s.collection.InsertMany(sessCtx, docs)
		if insertErr != nil {
			return nil, fmt.Errorf("insert messages: %w", insertErr)
		}

		messages = make([]store.Message, len(result.InsertedIDs))
		for i, insertedID := range result.InsertedIDs {
			if oid, ok := insertedID.(bson.ObjectID); ok {
				docRefs[i].ID = oid
			}
			messages[i] = docToMessage(docRefs[i])
		}
		return nil, nil
	})

	if txErr != nil {
		// If transaction failed due to unsupported topology, fall back
		if isTransactionNotSupported(txErr) {
			return s.insertManyFallback(ctx, docs, docRefs)
		}
		return nil, txErr
	}

	return messages, nil
}

// insertManyFallback performs a non-transactional InsertMany for standalone deployments.
func (s *Store) insertManyFallback(ctx context.Context, docs []any, docRefs []*messageDoc) ([]store.Message, error) {
	result, err := s.collection.InsertMany(ctx, docs)
	if err != nil {
		return nil, fmt.Errorf("insert messages: %w", err)
	}

	messages := make([]store.Message, len(result.InsertedIDs))
	for i, insertedID := range result.InsertedIDs {
		if oid, ok := insertedID.(bson.ObjectID); ok {
			docRefs[i].ID = oid
		}
		messages[i] = docToMessage(docRefs[i])
	}
	return messages, nil
}

// isTransactionNotSupported checks if the error indicates transactions aren't supported.
func isTransactionNotSupported(err error) bool {
	if err == nil {
		return false
	}
	// MongoDB returns code 263 (OperationNotSupportedInTransaction) or
	// "Transaction numbers are only allowed on..." for standalone servers
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == 263 || cmdErr.Code == 20
	}
	return false
}

// CreateMessageIdempotent atomically creates a message or returns existing.
//
// Uses MongoDB's findOneAndUpdate with upsert for atomic check-and-create.
// The unique index on (owner_id, idempotency_key) ensures only one document
// can exist for a given combination.
//
// This eliminates the need for distributed locks when handling duplicate
// requests (e.g., network retries, user double-clicks).
func (s *Store) CreateMessageIdempotent(ctx context.Context, data store.MessageData, idempotencyKey string) (store.Message, bool, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, false, store.ErrNotConnected
	}
	if idempotencyKey == "" {
		return nil, false, store.ErrInvalidIdempotencyKey
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()

	// Build the document to insert if not exists
	doc := &messageDoc{
		OwnerID:        data.OwnerID,
		SenderID:       data.SenderID,
		RecipientIDs:   data.RecipientIDs,
		Subject:        data.Subject,
		Body:           data.Body,
		Metadata:       data.Metadata,
		Status:         string(data.Status),
		FolderID:       data.FolderID,
		Tags:           data.Tags,
		CreatedAt:      now,
		UpdatedAt:      now,
		IsDraft:        false,
		IdempotencyKey: idempotencyKey,
		ThreadID:       data.ThreadID,
		ReplyToID:      data.ReplyToID,
	}

	if len(data.Attachments) > 0 {
		doc.Attachments = make([]attachmentDoc, len(data.Attachments))
		for i, a := range data.Attachments {
			doc.Attachments[i] = attachmentDoc{
				ID:          a.GetID(),
				Filename:    a.GetFilename(),
				ContentType: a.GetContentType(),
				Size:        a.GetSize(),
				URI:         a.GetURI(),
				CreatedAt:   a.GetCreatedAt(),
			}
		}
	}

	// Use findOneAndUpdate with upsert for atomic operation
	// $setOnInsert only sets fields on insert, not on update (existing doc)
	filter := bson.M{
		"owner_id":        data.OwnerID,
		"idempotency_key": idempotencyKey,
	}

	update := bson.M{
		"$setOnInsert": doc,
	}

	opts := mongoopts.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(mongoopts.After)

	var result messageDoc
	err := s.collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)
	if err != nil {
		return nil, false, fmt.Errorf("idempotent create: %w", err)
	}

	// Determine if this was an insert or an existing document
	// If created_at matches what we sent, it was inserted
	created := result.CreatedAt.Equal(now)

	return docToMessage(&result), created, nil
}

// =============================================================================
// Maintenance Operations
// =============================================================================

// DeleteExpiredTrash atomically deletes all messages in trash older than cutoff.
//
// Uses MongoDB's deleteMany which is atomic per-document. Multiple instances
// can safely call this concurrently - each message is deleted exactly once.
//
// No distributed locks needed - the database handles atomicity.
func (s *Store) DeleteExpiredTrash(ctx context.Context, cutoff time.Time) (int64, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return 0, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	// Atomic bulk delete - database ensures each document is deleted exactly once
	// even if multiple instances call this simultaneously
	filter := bson.M{
		"folder_id":  store.FolderTrash,
		"updated_at": bson.M{"$lt": cutoff},
	}

	result, err := s.collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("delete expired trash: %w", err)
	}

	return result.DeletedCount, nil
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
