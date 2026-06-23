// Package mongo provides a MongoDB implementation of store.Store.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

	// bgCancel stops the background index-build goroutine; bgWG lets Close wait
	// for it to return so it does not outlive the store (or, in tests, race a
	// per-test collection drop).
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup
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

	// Build the idempotency unique index synchronously. It is correctness-critical:
	// until it exists, concurrent CreateMessageIdempotent upserts on the same
	// (owner_id, idempotency_key) can both insert, so a failure here aborts Connect.
	if err := s.ensureIdempotencyIndex(ctx); err != nil {
		atomic.StoreInt32(&s.connected, 0)
		return fmt.Errorf("ensure idempotency index: %w", err)
	}

	// Secondary, performance, and text indexes only affect query speed, not
	// correctness, and can take a long time to build on large collections — create
	// them in the background so they do not block service startup. The build is
	// tied to a cancelable context so Close can signal it to stop from outliving
	// the store.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	s.bgCancel = bgCancel
	s.bgWG.Add(1)
	go func() { // #nosec G118 — background goroutine intentionally outlives request context
		defer s.bgWG.Done()
		if err := s.ensureSecondaryIndexes(bgCtx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Error("failed to ensure indexes", "error", err)
		}
	}()

	s.logger.Info("connected to MongoDB", "database", s.opts.database, "collection", s.opts.collection)
	return nil
}

// parseID converts a hex string to an ObjectID. An empty id is rejected with
// ErrInvalidID, matching the in-memory backend. A non-empty but malformed id
// cannot correspond to any stored document, so it is reported as ErrNotFound for
// cross-backend consistency (the in-memory and PostgreSQL backends treat any
// absent id the same way, regardless of its format).
func parseID(id string) (bson.ObjectID, error) {
	if id == "" {
		return bson.ObjectID{}, store.ErrInvalidID
	}
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return bson.ObjectID{}, store.ErrNotFound
	}
	return oid, nil
}

// Close marks the store as disconnected and stops the background index build.
// The caller is responsible for closing the MongoDB client.
func (s *Store) Close(ctx context.Context) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil
	}
	atomic.StoreInt32(&s.connected, 0)
	if s.bgCancel != nil {
		s.bgCancel()
	}
	s.bgWG.Wait()
	return nil
}

// ensureIdempotencyIndex creates the correctness-critical unique index on
// (owner_id, idempotency_key) for non-null keys. It is what makes
// CreateMessageIdempotent's upsert atomic across concurrent callers, so it is
// built synchronously in Connect and its error is surfaced to the caller.
func (s *Store) ensureIdempotencyIndex(ctx context.Context) error {
	idx := mongo.IndexModel{
		Keys: bson.D{
			bson.E{Key: "owner_id", Value: 1},
			bson.E{Key: "idempotency_key", Value: 1},
		},
		Options: mongoopts.Index().
			SetUnique(true).
			SetPartialFilterExpression(bson.M{"idempotency_key": bson.M{"$exists": true}}),
	}
	if _, err := s.collection.Indexes().CreateOne(ctx, idx); err != nil {
		return err
	}
	return nil
}

// ensureSecondaryIndexes creates non-correctness indexes (performance, text, and
// outbox indexes) in the background. These only affect query speed, so failures
// are logged rather than surfaced to Connect.
func (s *Store) ensureSecondaryIndexes(ctx context.Context) error {
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
		// Trash cleanup index: for efficient expired trash deletion
		{Keys: bson.D{
			bson.E{Key: "folder_id", Value: 1},
			bson.E{Key: "updated_at", Value: 1},
		}},
		// Compound index for folder listing queries (Find, Count, FindWithCount).
		// Includes __is_draft to fully cover the { $ne: true } filter that all
		// message queries add, avoiding a post-index scan on large collections.
		{Keys: bson.D{
			bson.E{Key: "owner_id", Value: 1},
			bson.E{Key: "folder_id", Value: 1},
			bson.E{Key: "__is_draft", Value: 1},
			bson.E{Key: "created_at", Value: -1},
		}},
		// Draft listing index
		{Keys: bson.D{
			bson.E{Key: "owner_id", Value: 1},
			bson.E{Key: "__is_draft", Value: 1},
			bson.E{Key: "created_at", Value: -1},
		}},
		// Thread, reply, and external ID indexes (sparse)
		{Keys: bson.D{bson.E{Key: "thread_id", Value: 1}}, Options: mongoopts.Index().SetSparse(true)},
		{Keys: bson.D{bson.E{Key: "reply_to_id", Value: 1}}, Options: mongoopts.Index().SetSparse(true)},
		{Keys: bson.D{bson.E{Key: "external_id", Value: 1}}, Options: mongoopts.Index().SetSparse(true)},
		// Per-message TTL index (sparse - only indexes documents with expires_at set)
		{Keys: bson.D{bson.E{Key: "expires_at", Value: 1}}, Options: mongoopts.Index().SetSparse(true)},
		// Scheduled delivery index (sparse - only indexes documents with available_at set)
		{Keys: bson.D{bson.E{Key: "available_at", Value: 1}}, Options: mongoopts.Index().SetSparse(true)},
	}

	_, err := s.collection.Indexes().CreateMany(ctx, indexes)
	if err != nil {
		return err
	}

	// Full-text search index: a single text index covering subject and body.
	// MongoDB allows only one text index per collection.
	if s.opts.enableFTS {
		textIndex := mongo.IndexModel{
			Keys: bson.D{
				bson.E{Key: "subject", Value: "text"},
				bson.E{Key: "body", Value: "text"},
			},
			Options: mongoopts.Index().SetName("idx_fts"),
		}
		if _, err := s.collection.Indexes().CreateOne(ctx, textIndex); err != nil {
			s.logger.Warn("failed to create text index for FTS", "error", err)
		}
	}

	// Create outbox collection index if outbox is enabled.
	if s.opts.outboxEnabled {
		outboxColl := s.db.Collection(s.opts.outboxCollection)
		outboxIndexes := []mongo.IndexModel{
			{Keys: bson.D{
				bson.E{Key: "status", Value: 1},
				bson.E{Key: "created_at", Value: 1},
			}},
		}
		if _, err := outboxColl.Indexes().CreateMany(ctx, outboxIndexes); err != nil {
			s.logger.Warn("failed to create outbox indexes", "error", err)
		}
	}

	return nil
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

	oid, err := parseID(id)
	if err != nil {
		return nil, err
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

	oid, err := parseID(id)
	if err != nil {
		return err
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
		// Over-fetch one extra row so we can reliably detect whether more
		// results exist beyond this page (mirrors Find/Search).
		findOpts.SetLimit(int64(opts.Limit) + 1)
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

	// Detect "has more" via the over-fetched extra row, then trim it off.
	hasMore := opts.Limit > 0 && len(docs) > opts.Limit
	if hasMore {
		docs = docs[:opts.Limit]
	}

	drafts := make([]store.DraftMessage, len(docs))
	for i := range docs {
		drafts[i] = docToMessage(&docs[i])
	}

	return &store.DraftList{
		Drafts:  drafts,
		Total:   total,
		HasMore: hasMore,
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
	if len(msg.delta.headers) > 0 {
		for k, v := range msg.delta.headers {
			set["headers."+k] = v
		}
	}
	if len(msg.delta.metadata) > 0 {
		for k, v := range msg.delta.metadata {
			set["metadata."+k] = v
		}
	}
	if msg.delta.threadID != nil {
		set["thread_id"] = *msg.delta.threadID
	}
	if msg.delta.replyToID != nil {
		set["reply_to_id"] = *msg.delta.replyToID
	}
	if msg.delta.externalID != nil {
		set["external_id"] = *msg.delta.externalID
	}
	if msg.delta.expiresAtSet {
		set["expires_at"] = msg.delta.expiresAt
	}
	if msg.delta.availableAtSet {
		set["available_at"] = msg.delta.availableAt
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
