// Package mongo provides a MongoDB implementation of store.Store.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// regexMetaChars matches regex metacharacters that need escaping.
var regexMetaChars = regexp.MustCompile(`[\\^$.|?*+()[\]{}]`)

// escapeRegex escapes regex metacharacters in a string to prevent regex injection.
func escapeRegex(s string) string {
	return regexMetaChars.ReplaceAllString(s, `\$0`)
}

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
	defer cursor.Close(ctx)

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

// =============================================================================
// Message Operations
// =============================================================================

// Get retrieves a message by ID.
func (s *Store) Get(ctx context.Context, id string) (store.Message, error) {
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
		"__is_draft": bson.M{"$ne": true},
	}

	var doc messageDoc
	err = s.collection.FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("find message: %w", err)
	}

	return docToMessage(&doc), nil
}

// Find retrieves messages matching the filters.
func (s *Store) Find(ctx context.Context, filters []store.Filter, opts store.ListOptions) (*store.MessageList, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	filter := buildFilter(filters)
	filter["__is_draft"] = bson.M{"$ne": true}

	// Determine sort key and direction
	sortKey := "created_at"
	sortDir := -1 // DESC
	if opts.SortBy != "" {
		if key, ok := store.MessageFieldKey(opts.SortBy); ok {
			sortKey = mapKey(key)
		}
	}
	if opts.SortOrder == store.SortAsc {
		sortDir = 1
	}

	// Cursor-based pagination: use keyset filtering when StartAfter is provided
	if opts.StartAfter != "" {
		cursorOID, cursorErr := bson.ObjectIDFromHex(opts.StartAfter)
		if cursorErr != nil {
			return nil, store.ErrInvalidID
		}
		// Fetch cursor document's sort field value
		var cursorDoc messageDoc
		cursorErr = s.collection.FindOne(ctx, bson.M{"_id": cursorOID}).Decode(&cursorDoc)
		if cursorErr != nil {
			if errors.Is(cursorErr, mongo.ErrNoDocuments) {
				return nil, store.ErrNotFound
			}
			return nil, fmt.Errorf("fetch cursor document: %w", cursorErr)
		}
		// Get the sort field value from cursor document
		var cursorSortValue any
		switch sortKey {
		case "created_at":
			cursorSortValue = cursorDoc.CreatedAt
		case "updated_at":
			cursorSortValue = cursorDoc.UpdatedAt
		default:
			cursorSortValue = cursorDoc.CreatedAt
		}
		// Add keyset condition: (sortField < cursorValue) OR (sortField == cursorValue AND _id < cursorOID) for DESC
		// For ASC: (sortField > cursorValue) OR (sortField == cursorValue AND _id > cursorOID)
		comp := "$lt"
		if sortDir == 1 {
			comp = "$gt"
		}
		filter["$or"] = []bson.M{
			{sortKey: bson.M{comp: cursorSortValue}},
			{sortKey: cursorSortValue, "_id": bson.M{comp: cursorOID}},
		}
	}

	findOpts := mongoopts.Find()
	if opts.Limit > 0 {
		findOpts.SetLimit(int64(opts.Limit))
	}
	if opts.StartAfter == "" && opts.Offset > 0 {
		findOpts.SetSkip(int64(opts.Offset))
	}
	findOpts.SetSort(bson.D{
		bson.E{Key: sortKey, Value: sortDir},
		bson.E{Key: "_id", Value: sortDir},
	})

	// Get total count for pagination (without cursor filter to get true total)
	countFilter := bson.M{}
	for k, v := range filter {
		if k != "$or" {
			countFilter[k] = v
		}
	}
	total, err := s.collection.CountDocuments(ctx, countFilter)
	if err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}

	cursor, err := s.collection.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, fmt.Errorf("find messages: %w", err)
	}
	defer cursor.Close(ctx)

	var docs []messageDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}

	messages := make([]store.Message, len(docs))
	for i := range docs {
		messages[i] = docToMessage(&docs[i])
	}

	return &store.MessageList{
		Messages: messages,
		Total:    total,
		HasMore:  opts.Limit > 0 && len(messages) >= opts.Limit,
	}, nil
}

// Count counts messages matching the filters.
func (s *Store) Count(ctx context.Context, filters []store.Filter) (int64, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return 0, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	filter := buildFilter(filters)
	filter["__is_draft"] = bson.M{"$ne": true}

	count, err := s.collection.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}

	return count, nil
}

// Search performs full-text search on messages.
func (s *Store) Search(ctx context.Context, query store.SearchQuery) (*store.MessageList, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	filter := buildFilter(query.Filters)
	filter["__is_draft"] = bson.M{"$ne": true}

	if query.OwnerID != "" {
		filter["owner_id"] = query.OwnerID
	}

	// Text search using regex on subject and body
	if query.Query != "" {
		if !s.opts.enableRegex {
			return nil, store.ErrRegexSearchDisabled
		}

		searchFields := query.Fields
		if len(searchFields) == 0 {
			searchFields = []string{"subject", "body"}
		}

		escapedQuery := escapeRegex(query.Query)

		orConditions := make([]bson.M, 0, len(searchFields))
		for _, field := range searchFields {
			orConditions = append(orConditions, bson.M{
				field: bson.M{"$regex": escapedQuery, "$options": "i"},
			})
		}
		filter["$or"] = orConditions
	}

	// Tag filtering
	if len(query.Tags) > 0 {
		filter["tags"] = bson.M{"$all": query.Tags}
	}

	// Count total before applying cursor filter (for true total)
	total, err := s.collection.CountDocuments(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("count search results: %w", err)
	}

	// Cursor-based pagination: add keyset filter when StartAfter is provided.
	// Since the text search may already use $or, we wrap the cursor condition
	// with $and to avoid conflicts.
	if query.Options.StartAfter != "" {
		cursorOID, cursorErr := bson.ObjectIDFromHex(query.Options.StartAfter)
		if cursorErr != nil {
			return nil, store.ErrInvalidID
		}
		var cursorDoc messageDoc
		cursorErr = s.collection.FindOne(ctx, bson.M{"_id": cursorOID}).Decode(&cursorDoc)
		if cursorErr != nil {
			if errors.Is(cursorErr, mongo.ErrNoDocuments) {
				return nil, store.ErrNotFound
			}
			return nil, fmt.Errorf("fetch cursor document: %w", cursorErr)
		}
		// Build keyset condition for DESC sort on created_at
		cursorCond := []bson.M{
			{"created_at": bson.M{"$lt": cursorDoc.CreatedAt}},
			{"created_at": cursorDoc.CreatedAt, "_id": bson.M{"$lt": cursorOID}},
		}
		// If filter already has $or (from text search), use $and to combine
		if existingOr, hasOr := filter["$or"]; hasOr {
			delete(filter, "$or")
			filter["$and"] = []bson.M{
				{"$or": existingOr.([]bson.M)},
				{"$or": cursorCond},
			}
		} else {
			filter["$or"] = cursorCond
		}
	}

	findOpts := mongoopts.Find()
	if query.Options.Limit > 0 {
		findOpts.SetLimit(int64(query.Options.Limit))
	}
	if query.Options.StartAfter == "" && query.Options.Offset > 0 {
		findOpts.SetSkip(int64(query.Options.Offset))
	}
	findOpts.SetSort(bson.D{
		bson.E{Key: "created_at", Value: -1},
		bson.E{Key: "_id", Value: -1},
	})

	cursor, err := s.collection.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer cursor.Close(ctx)

	var docs []messageDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}

	messages := make([]store.Message, len(docs))
	for i := range docs {
		messages[i] = docToMessage(&docs[i])
	}

	var nextCursor string
	if query.Options.Limit > 0 && len(messages) >= query.Options.Limit {
		nextCursor = messages[len(messages)-1].GetID()
	}

	return &store.MessageList{
		Messages:   messages,
		Total:      total,
		HasMore:    query.Options.Limit > 0 && len(messages) >= query.Options.Limit,
		NextCursor: nextCursor,
	}, nil
}

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
					"unread": bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$is_read", false}}, 1, 0}}},
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

// =============================================================================
// Internal types
// =============================================================================

// messageDoc is the MongoDB document representation.
type messageDoc struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	OwnerID        string             `bson:"owner_id"`
	SenderID       string             `bson:"sender_id"`
	RecipientIDs   []string           `bson:"recipient_ids"`
	Subject        string             `bson:"subject"`
	Body           string             `bson:"body"`
	Metadata       map[string]any     `bson:"metadata,omitempty"`
	Status         string             `bson:"status"`
	IsRead         bool               `bson:"is_read"`
	ReadAt         *time.Time         `bson:"read_at,omitempty"`
	FolderID       string             `bson:"folder_id"`
	Tags           []string           `bson:"tags,omitempty"`
	Attachments    []attachmentDoc    `bson:"attachments,omitempty"`
	CreatedAt      time.Time          `bson:"created_at"`
	UpdatedAt      time.Time          `bson:"updated_at"`
	IsDraft        bool               `bson:"__is_draft,omitempty"`
	IdempotencyKey string             `bson:"idempotency_key,omitempty"` // For atomic idempotent creates
	ThreadID       string             `bson:"thread_id,omitempty"`
	ReplyToID      string             `bson:"reply_to_id,omitempty"`
}

// attachmentDoc is the MongoDB document for attachments.
type attachmentDoc struct {
	ID          string    `bson:"id"`
	Filename    string    `bson:"filename"`
	ContentType string    `bson:"content_type"`
	Size        int64     `bson:"size"`
	URI         string    `bson:"uri"`
	CreatedAt   time.Time `bson:"created_at"`
}

// message implements both store.Message and store.DraftMessage for MongoDB.
type message struct {
	id               string
	ownerID          string
	senderID         string
	recipientIDs     []string
	subject          string
	body             string
	metadata         map[string]any
	status           store.MessageStatus
	isRead           bool
	readAt           *time.Time
	folderID         string
	tags             []string
	attachments      []*attachment
	createdAt        time.Time
	updatedAt        time.Time
	isDraft          bool
	threadID         string
	replyToID        string

	// delta tracking (internal use only)
	delta messageDelta
}

// messageDelta tracks changes for efficient updates.
type messageDelta struct {
	subject      *string
	body         *string
	recipientIDs []string
	recipientsSet bool
	metadata     map[string]any
}

// attachment implements store.Attachment for MongoDB.
type attachment struct {
	id          string
	filename    string
	contentType string
	size        int64
	uri         string
	createdAt   time.Time
}

// =============================================================================
// Message getters (implements store.Message)
// =============================================================================

func (m *message) GetID() string                             { return m.id }
func (m *message) GetOwnerID() string                        { return m.ownerID }
func (m *message) GetSenderID() string                       { return m.senderID }
func (m *message) GetRecipientIDs() []string                 { return m.recipientIDs }
func (m *message) GetSubject() string                        { return m.subject }
func (m *message) GetBody() string                           { return m.body }
func (m *message) GetMetadata() map[string]any               { return m.metadata }
func (m *message) GetStatus() store.MessageStatus            { return m.status }
func (m *message) GetIsRead() bool                           { return m.isRead }
func (m *message) GetReadAt() *time.Time                     { return m.readAt }
func (m *message) GetFolderID() string                       { return m.folderID }
func (m *message) GetTags() []string                         { return m.tags }
func (m *message) GetCreatedAt() time.Time                   { return m.createdAt }
func (m *message) GetUpdatedAt() time.Time                   { return m.updatedAt }
func (m *message) GetThreadID() string                       { return m.threadID }
func (m *message) GetReplyToID() string                      { return m.replyToID }
func (m *message) GetAttachments() []store.Attachment {
	if m.attachments == nil {
		return nil
	}
	result := make([]store.Attachment, len(m.attachments))
	for i, a := range m.attachments {
		result[i] = a
	}
	return result
}

// =============================================================================
// Draft setters (implements store.DraftMessage fluent API)
// =============================================================================

func (m *message) SetSubject(subject string) store.DraftMessage {
	m.subject = subject
	m.delta.subject = &subject
	return m
}

func (m *message) SetBody(body string) store.DraftMessage {
	m.body = body
	m.delta.body = &body
	return m
}

func (m *message) SetRecipients(recipientIDs ...string) store.DraftMessage {
	m.recipientIDs = recipientIDs
	m.delta.recipientIDs = recipientIDs
	m.delta.recipientsSet = true
	return m
}

func (m *message) SetMetadata(key string, value any) store.DraftMessage {
	if m.metadata == nil {
		m.metadata = make(map[string]any)
	}
	if m.delta.metadata == nil {
		m.delta.metadata = make(map[string]any)
	}
	m.metadata[key] = value
	m.delta.metadata[key] = value
	return m
}

func (m *message) AddAttachment(att store.Attachment) store.DraftMessage {
	if att == nil {
		return m
	}
	a := &attachment{
		id:          att.GetID(),
		filename:    att.GetFilename(),
		contentType: att.GetContentType(),
		size:        att.GetSize(),
		uri:         att.GetURI(),
		createdAt:   att.GetCreatedAt(),
	}
	m.attachments = append(m.attachments, a)
	return m
}

// =============================================================================
// Internal delta tracking methods
// =============================================================================

func (m *message) hasChanges() bool {
	return m.delta.subject != nil ||
		m.delta.body != nil ||
		m.delta.recipientsSet ||
		len(m.delta.metadata) > 0
}

func (m *message) resetDelta() {
	m.delta = messageDelta{}
}

// =============================================================================
// Attachment getters
// =============================================================================

func (a *attachment) GetID() string          { return a.id }
func (a *attachment) GetFilename() string    { return a.filename }
func (a *attachment) GetContentType() string { return a.contentType }
func (a *attachment) GetSize() int64         { return a.size }
func (a *attachment) GetURI() string         { return a.uri }
func (a *attachment) GetCreatedAt() time.Time { return a.createdAt }

// =============================================================================
// Conversion functions
// =============================================================================

func messageToDoc(msg *message) *messageDoc {
	doc := &messageDoc{
		OwnerID:      msg.ownerID,
		SenderID:     msg.senderID,
		RecipientIDs: msg.recipientIDs,
		Subject:      msg.subject,
		Body:         msg.body,
		Metadata:     msg.metadata,
		Status:       string(msg.status),
		IsRead:       msg.isRead,
		ReadAt:       msg.readAt,
		FolderID:     msg.folderID,
		Tags:         msg.tags,
		CreatedAt:    msg.createdAt,
		UpdatedAt:    msg.updatedAt,
		IsDraft:      msg.isDraft,
		ThreadID:     msg.threadID,
		ReplyToID:    msg.replyToID,
	}

	if len(msg.attachments) > 0 {
		doc.Attachments = make([]attachmentDoc, len(msg.attachments))
		for i, a := range msg.attachments {
			doc.Attachments[i] = attachmentDoc{
				ID:          a.id,
				Filename:    a.filename,
				ContentType: a.contentType,
				Size:        a.size,
				URI:         a.uri,
				CreatedAt:   a.createdAt,
			}
		}
	}

	if msg.id != "" {
		if oid, err := bson.ObjectIDFromHex(msg.id); err == nil {
			doc.ID = oid
		}
	}
	return doc
}

func docToMessage(doc *messageDoc) *message {
	msg := &message{
		id:           doc.ID.Hex(),
		ownerID:      doc.OwnerID,
		senderID:     doc.SenderID,
		recipientIDs: doc.RecipientIDs,
		subject:      doc.Subject,
		body:         doc.Body,
		metadata:     doc.Metadata,
		status:       store.MessageStatus(doc.Status),
		isRead:       doc.IsRead,
		readAt:       doc.ReadAt,
		folderID:     doc.FolderID,
		tags:         doc.Tags,
		createdAt:    doc.CreatedAt,
		updatedAt:    doc.UpdatedAt,
		isDraft:      doc.IsDraft,
		threadID:     doc.ThreadID,
		replyToID:    doc.ReplyToID,
	}

	if len(doc.Attachments) > 0 {
		msg.attachments = make([]*attachment, len(doc.Attachments))
		for i, a := range doc.Attachments {
			msg.attachments[i] = &attachment{
				id:          a.ID,
				filename:    a.Filename,
				contentType: a.ContentType,
				size:        a.Size,
				uri:         a.URI,
				createdAt:   a.CreatedAt,
			}
		}
	}

	return msg
}

// mapKey translates shared filter keys to MongoDB field names.
func mapKey(key string) string {
	if key == "id" {
		return "_id"
	}
	return key
}

func buildFilter(filters []store.Filter) bson.M {
	if len(filters) == 0 {
		return bson.M{}
	}

	result := bson.M{}
	for _, f := range filters {
		if f == nil {
			continue
		}
		key := mapKey(f.Key())
		value := f.Value()
		op := f.Operator()

		switch op {
		case "eq":
			result[key] = value
		case "ne":
			result[key] = bson.M{"$ne": value}
		case "gt":
			result[key] = bson.M{"$gt": value}
		case "gte":
			result[key] = bson.M{"$gte": value}
		case "lt":
			result[key] = bson.M{"$lt": value}
		case "lte":
			result[key] = bson.M{"$lte": value}
		case "in":
			result[key] = bson.M{"$in": value}
		case "nin":
			result[key] = bson.M{"$nin": value}
		case "exists":
			result[key] = bson.M{"$exists": value}
		case "contains":
			result[key] = value // MongoDB arrays automatically check contains
		}
	}

	return result
}

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

// Compile-time checks
var _ store.Store = (*Store)(nil)
var _ store.Message = (*message)(nil)
var _ store.DraftMessage = (*message)(nil)
var _ store.Attachment = (*attachment)(nil)
