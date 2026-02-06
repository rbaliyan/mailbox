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
