// Package mongo provides a MongoDB implementation of store.Store.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Compile-time checks
var (
	_ store.Store           = (*Store)(nil)
	_ store.FolderCounter   = (*Store)(nil)
	_ store.FindWithCounter = (*Store)(nil)
	_ store.FolderLister    = (*Store)(nil)
)

// Store implements store.Store using MongoDB.
type Store struct {
	client     *mongo.Client
	db         *mongo.Database
	collection *mongo.Collection
	opts       *options
	connected  int32
	logger     *slog.Logger
}

// New creates a new MongoDB store with the provided client.
// Call Connect() to initialize the collection and indexes.
func New(client *mongo.Client, opts ...Option) *Store {
	o := newOptions(opts...)
	return &Store{
		client: client,
		opts:   o,
		logger: o.logger,
	}
}

// Connect initializes the database, collection, and indexes.
func (s *Store) Connect(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.connected, 0, 1) {
		return store.ErrAlreadyConnected
	}

	if s.client == nil {
		atomic.StoreInt32(&s.connected, 0)
		return fmt.Errorf("mongo: client is required")
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	if err := s.client.Ping(ctx, nil); err != nil {
		atomic.StoreInt32(&s.connected, 0)
		return fmt.Errorf("mongo ping: %w", err)
	}

	s.db = s.client.Database(s.opts.database)
	s.collection = s.db.Collection(s.opts.collection)

	if err := s.ensureIndexes(ctx); err != nil {
		atomic.StoreInt32(&s.connected, 0)
		return fmt.Errorf("ensure indexes: %w", err)
	}

	s.logger.Info("connected to MongoDB", "database", s.opts.database, "collection", s.opts.collection)
	return nil
}

// Close marks the store as disconnected.
// The caller is responsible for closing the MongoDB client.
func (s *Store) Close(ctx context.Context) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil
	}
	atomic.StoreInt32(&s.connected, 0)
	return nil
}

// ensureIndexes creates required indexes.
func (s *Store) ensureIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{Keys: bson.D{bson.E{Key: "owner_id", Value: 1}}},
		{Keys: bson.D{bson.E{Key: "sender_id", Value: 1}}},
		{Keys: bson.D{bson.E{Key: "recipient_ids", Value: 1}}},
		{Keys: bson.D{bson.E{Key: "status", Value: 1}}},
		{Keys: bson.D{bson.E{Key: "is_read", Value: 1}}},
		{Keys: bson.D{bson.E{Key: "folder_id", Value: 1}}},
		{Keys: bson.D{bson.E{Key: "tags", Value: 1}}},
		{Keys: bson.D{bson.E{Key: "created_at", Value: -1}}},
		{Keys: bson.D{bson.E{Key: "__is_draft", Value: 1}}},
		// Idempotency index: unique constraint on (owner_id, idempotency_key) for non-null keys
		// This enables atomic idempotent message creation without distributed locks
		{
			Keys: bson.D{
				bson.E{Key: "owner_id", Value: 1},
				bson.E{Key: "idempotency_key", Value: 1},
			},
			Options: mongoopts.Index().
				SetUnique(true).
				SetPartialFilterExpression(bson.M{"idempotency_key": bson.M{"$exists": true}}),
		},
		// Trash cleanup index: for efficient expired trash deletion
		{Keys: bson.D{
			bson.E{Key: "folder_id", Value: 1},
			bson.E{Key: "updated_at", Value: 1},
		}},
		// Compound indexes for common queries
		{Keys: bson.D{
			bson.E{Key: "owner_id", Value: 1},
			bson.E{Key: "folder_id", Value: 1},
			bson.E{Key: "created_at", Value: -1},
		}},
		// Draft listing index
		{Keys: bson.D{
			bson.E{Key: "owner_id", Value: 1},
			bson.E{Key: "__is_draft", Value: 1},
			bson.E{Key: "created_at", Value: -1},
		}},
	}

	_, err := s.collection.Indexes().CreateMany(ctx, indexes)
	return err
}

// =============================================================================
// Draft Operations
// =============================================================================

// NewDraft creates a new empty draft for the given owner.
func (s *Store) NewDraft(ownerID string) store.DraftMessage {
	return &message{
		ownerID:  ownerID,
		senderID: ownerID,
		isDraft:  true,
	}
}

// GetDraft retrieves a draft by ID.
func (s *Store) GetDraft(ctx context.Context, id string) (store.DraftMessage, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return nil, store.ErrInvalidID
	}

	filter := bson.M{
		"_id":        oid,
		"__is_draft": true,
	}

	var doc messageDoc
	err = s.collection.FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("find draft: %w", err)
	}

	return docToMessage(&doc), nil
}

// SaveDraft persists a draft.
func (s *Store) SaveDraft(ctx context.Context, draft store.DraftMessage) (store.DraftMessage, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	// Verify this is a draft from this store
	msg, ok := draft.(*message)
	if !ok {
		return nil, fmt.Errorf("mongo: draft must be created by this store")
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()
	msg.updatedAt = now
	msg.isDraft = true

	if msg.id == "" {
		// New draft - insert
		if msg.createdAt.IsZero() {
			msg.createdAt = now
		}
		if msg.status == "" {
			msg.status = store.MessageStatusDraft
		}

		doc := messageToDoc(msg)
		result, err := s.collection.InsertOne(ctx, doc)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return nil, store.ErrDuplicateEntry
			}
			return nil, fmt.Errorf("insert draft: %w", err)
		}

		if oid, ok := result.InsertedID.(bson.ObjectID); ok {
			msg.id = oid.Hex()
		}
	} else {
		// Existing draft - update
		oid, err := bson.ObjectIDFromHex(msg.id)
		if err != nil {
			return nil, store.ErrInvalidID
		}

		if !msg.hasChanges() {
			return msg, nil
		}

		update := buildDraftUpdate(msg)
		filter := bson.M{
			"_id":        oid,
			"__is_draft": true,
		}

		result, err := s.collection.UpdateOne(ctx, filter, update)
		if err != nil {
			return nil, fmt.Errorf("update draft: %w", err)
		}

		if result.MatchedCount == 0 {
			return nil, store.ErrNotFound
		}
	}

	msg.resetDelta()
	return msg, nil
}

// DeleteDraft permanently removes a draft.
func (s *Store) DeleteDraft(ctx context.Context, id string) error {
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
		"__is_draft": true,
	}

	result, err := s.collection.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete draft: %w", err)
	}

	if result.DeletedCount == 0 {
		return store.ErrNotFound
	}

	return nil
}

// ListDrafts returns all drafts for a user.
func (s *Store) ListDrafts(ctx context.Context, ownerID string, opts store.ListOptions) (*store.DraftList, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	filter := bson.M{
		"owner_id":   ownerID,
		"__is_draft": true,
	}

	findOpts := mongoopts.Find()
	if opts.Limit > 0 {
		findOpts.SetLimit(int64(opts.Limit))
	}
	if opts.Offset > 0 {
		findOpts.SetSkip(int64(opts.Offset))
	}
	findOpts.SetSort(bson.D{bson.E{Key: "created_at", Value: -1}})

	// Get total count for pagination
	total, err := s.collection.CountDocuments(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("count drafts: %w", err)
	}

	cursor, err := s.collection.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, fmt.Errorf("find drafts: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var docs []messageDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode drafts: %w", err)
	}

	drafts := make([]store.DraftMessage, len(docs))
	for i := range docs {
		drafts[i] = docToMessage(&docs[i])
	}

	return &store.DraftList{
		Drafts:  drafts,
		Total:   total,
		HasMore: opts.Limit > 0 && len(drafts) >= opts.Limit,
	}, nil
}

// buildDraftUpdate constructs the MongoDB update document for a draft.
func buildDraftUpdate(msg *message) bson.M {
	set := bson.M{
		"updated_at": msg.updatedAt,
	}

	if msg.delta.subject != nil {
		set["subject"] = *msg.delta.subject
	}
	if msg.delta.body != nil {
		set["body"] = *msg.delta.body
	}
	if msg.delta.recipientsSet {
		set["recipient_ids"] = msg.delta.recipientIDs
	}
	if len(msg.delta.metadata) > 0 {
		for k, v := range msg.delta.metadata {
			set["metadata."+k] = v
		}
	}

	// Always include full attachments list on update
	if msg.attachments != nil {
		attachments := make([]attachmentDoc, len(msg.attachments))
		for i, a := range msg.attachments {
			attachments[i] = attachmentDoc{
				ID:          a.id,
				Filename:    a.filename,
				ContentType: a.contentType,
				Size:        a.size,
				URI:         a.uri,
				CreatedAt:   a.createdAt,
			}
		}
		set["attachments"] = attachments
	}

	return bson.M{"$set": set}
}
