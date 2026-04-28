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

// toAttachmentDocs converts a slice of store.Attachment to a slice of attachmentDoc.
func toAttachmentDocs(attachments []store.Attachment) []attachmentDoc {
	docs := make([]attachmentDoc, len(attachments))
	for i, a := range attachments {
		docs[i] = attachmentDoc{
			ID:          a.GetID(),
			Filename:    a.GetFilename(),
			ContentType: a.GetContentType(),
			Size:        a.GetSize(),
			URI:         a.GetURI(),
			CreatedAt:   a.GetCreatedAt(),
		}
	}
	return docs
}

// newMessageDocFromData builds a messageDoc from MessageData and a timestamp.
func newMessageDocFromData(data store.MessageData, now time.Time) *messageDoc {
	doc := &messageDoc{
		OwnerID:      data.OwnerID,
		SenderID:     data.SenderID,
		RecipientIDs: data.RecipientIDs,
		Subject:      data.Subject,
		Body:         data.Body,
		Headers:      data.Headers,
		Metadata:     data.Metadata,
		Status:       string(data.Status),
		FolderID:     data.FolderID,
		Tags:         data.Tags,
		CreatedAt:    now,
		UpdatedAt:    now,
		IsDraft:      false,
		ThreadID:     data.ThreadID,
		ReplyToID:    data.ReplyToID,
		ExternalID:   data.ExternalID,
		ExpiresAt:    data.ExpiresAt,
		AvailableAt:  data.AvailableAt,
	}

	if len(data.Attachments) > 0 {
		doc.Attachments = toAttachmentDocs(data.Attachments)
	}

	return doc
}

// CreateMessage creates a new message from the given data.
func (s *Store) CreateMessage(ctx context.Context, data store.MessageData) (store.Message, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()
	doc := newMessageDocFromData(data, now)

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
		doc := newMessageDocFromData(d, now)
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
	doc := newMessageDocFromData(data, now)
	doc.IdempotencyKey = idempotencyKey

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

// CreateMessagesIdempotent creates multiple messages with idempotency keys.
// Uses pipelined findOneAndUpdate with upsert for batch efficiency.
func (s *Store) CreateMessagesIdempotent(ctx context.Context, entries []store.IdempotentCreateEntry) ([]store.IdempotentCreateResult, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	results := make([]store.IdempotentCreateResult, len(entries))
	for i, entry := range entries {
		msg, created, err := s.CreateMessageIdempotent(ctx, entry.Data, entry.IdempotencyKey)
		results[i] = store.IdempotentCreateResult{Message: msg, Created: created, Err: err}
	}
	return results, nil
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

// DeleteMessagesByIDs deletes the specified messages and returns the IDs
// that were actually deleted by this call. Uses a two-phase approach:
// find existing IDs, then deleteMany, then verify deletions. This reduces
// N sequential FindOneAndDelete calls to 3 round-trips regardless of batch size.
//
// In multi-instance environments, the "winner" determination has a small
// TOCTOU window between find and delete. For attachment cleanup, this means
// two instances may both believe they deleted a message and release refs.
// Since deleteMany is atomic per-document, the actual deletion happens once,
// and double ref-release is harmless (extra decrement on already-zero ref).
func (s *Store) DeleteMessagesByIDs(ctx context.Context, ids []string) ([]string, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}
	if len(ids) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	// Convert to ObjectIDs.
	oids := make([]bson.ObjectID, 0, len(ids))
	idMap := make(map[bson.ObjectID]string, len(ids))
	for _, id := range ids {
		oid, err := bson.ObjectIDFromHex(id)
		if err != nil {
			continue
		}
		oids = append(oids, oid)
		idMap[oid] = id
	}
	if len(oids) == 0 {
		return nil, nil
	}

	// Phase 1: Find which IDs currently exist.
	cursor, err := s.collection.Find(ctx, bson.M{
		"_id": bson.M{"$in": oids},
	}, mongoopts.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("find messages for deletion: %w", err)
	}
	var existing []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cursor.All(ctx, &existing); err != nil {
		return nil, fmt.Errorf("decode existing messages: %w", err)
	}
	if len(existing) == 0 {
		return nil, nil
	}

	existingOIDs := make([]bson.ObjectID, len(existing))
	for i, e := range existing {
		existingOIDs[i] = e.ID
	}

	// Phase 2: Bulk delete.
	_, err = s.collection.DeleteMany(ctx, bson.M{
		"_id": bson.M{"$in": existingOIDs},
	})
	if err != nil {
		return nil, fmt.Errorf("delete messages by IDs: %w", err)
	}

	// Phase 3: Verify which were actually deleted (not restored by another instance).
	cursor, err = s.collection.Find(ctx, bson.M{
		"_id": bson.M{"$in": existingOIDs},
	}, mongoopts.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		// If verification fails, assume all were deleted (optimistic).
		deleted := make([]string, 0, len(existingOIDs))
		for _, oid := range existingOIDs {
			deleted = append(deleted, idMap[oid])
		}
		return deleted, nil
	}
	var surviving []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cursor.All(ctx, &surviving); err != nil {
		deleted := make([]string, 0, len(existingOIDs))
		for _, oid := range existingOIDs {
			deleted = append(deleted, idMap[oid])
		}
		return deleted, nil
	}

	survivingSet := make(map[bson.ObjectID]bool, len(surviving))
	for _, s := range surviving {
		survivingSet[s.ID] = true
	}

	deleted := make([]string, 0, len(existingOIDs))
	for _, oid := range existingOIDs {
		if !survivingSet[oid] {
			deleted = append(deleted, idMap[oid])
		}
	}

	return deleted, nil
}

// DeleteTTLExpiredMessages atomically deletes all non-draft messages whose
// expires_at is non-null and before the given time.
func (s *Store) DeleteTTLExpiredMessages(ctx context.Context, now time.Time) (int64, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return 0, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	filter := bson.M{
		"__is_draft": bson.M{"$ne": true},
		"expires_at": bson.M{"$ne": nil, "$lt": now},
	}

	result, err := s.collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("delete TTL expired messages: %w", err)
	}

	return result.DeletedCount, nil
}

// DeleteExpiredMessages atomically deletes all non-draft messages older than cutoff.
func (s *Store) DeleteExpiredMessages(ctx context.Context, cutoff time.Time) (int64, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return 0, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	filter := bson.M{
		"__is_draft": bson.M{"$ne": true},
		"created_at": bson.M{"$lt": cutoff},
	}

	result, err := s.collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("delete expired messages: %w", err)
	}

	return result.DeletedCount, nil
}
