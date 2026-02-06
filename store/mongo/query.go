package mongo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"

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

// mapKey translates shared filter keys to MongoDB field names.
func mapKey(key string) string {
	if key == "id" {
		return "_id"
	}
	return key
}

// buildFilter converts a slice of store.Filter to a MongoDB filter document.
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
