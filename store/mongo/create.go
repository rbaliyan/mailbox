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
		Metadata:     data.Metadata,
		Status:       string(data.Status),
		FolderID:     data.FolderID,
		Tags:         data.Tags,
		CreatedAt:    now,
		UpdatedAt:    now,
		IsDraft:      false,
		ThreadID:     data.ThreadID,
		ReplyToID:    data.ReplyToID,
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
