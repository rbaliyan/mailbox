package mailbox

import (
	"context"
	"errors"

	"github.com/rbaliyan/mailbox/store"
)

// MessageIterator provides streaming access to messages.
// It implements a pull-based iteration pattern for memory-efficient processing
// of large result sets.
//
// # Iterator vs List: When to Use Each
//
// Use MessageIterator (Stream* methods) when:
//   - Processing large result sets where memory is a concern
//   - You need to process messages one at a time
//   - You want early termination without loading all data
//   - Building ETL pipelines or data exports
//
// Use MessageList (Inbox, Sent, etc.) when:
//   - Building paginated UIs with total counts
//   - You need bulk operations (MarkRead, Move, Delete all)
//   - Result sets are small and fit comfortably in memory
//   - You need random access to results
//
// Example comparison:
//
//	// Iterator: memory-efficient, process one at a time
//	iter, _ := mb.StreamInbox(ctx, StreamOptions{BatchSize: 100})
//	for hasNext, err := iter.Next(ctx); hasNext && err == nil; hasNext, err = iter.Next(ctx) {
//	    msg, _ := iter.Message()
//	    // process each message individually
//	}
//
//	// List: loads page into memory, supports bulk ops
//	list, _ := mb.Inbox(ctx, store.ListOptions{Limit: 50})
//	list.MarkRead(ctx)  // bulk operation on all 50 messages
//	fmt.Printf("Showing %d of %d total", len(list.All()), list.Total())
//
// # Iterator Usage
//
// The iterator is stateless and holds no resources requiring cleanup.
// Simply stop calling Next() when done - the GC handles the rest.
//
// Example:
//
//	iter, _ := mb.StreamInbox(ctx, StreamOptions{BatchSize: 100})
//
//	for {
//	    hasNext, err := iter.Next(ctx)
//	    if err != nil {
//	        // handle error (e.g., service disconnected, context cancelled)
//	        break
//	    }
//	    if !hasNext {
//	        break
//	    }
//	    msg, _ := iter.Message()
//	    // process message - can use msg.Update(), msg.Delete(), etc.
//	}
//
// ErrIteratorOutOfBounds is returned when Message() is called without a successful Next().
var ErrIteratorOutOfBounds = errors.New("mailbox: iterator out of bounds - call Next() first")

// MessageIterator provides streaming access to messages.
// Use Next() to advance, Message() to get current message.
//
// Ownership: MessageIterator holds no resources requiring cleanup.
// There is no Close method — simply stop calling Next() when done.
//
// Thread Safety: MessageIterator is NOT safe for concurrent use. Each iterator
// should be used by a single goroutine. If you need concurrent access, create
// separate iterators for each goroutine.
type MessageIterator interface {
	// Next advances to the next message.
	// Returns (true, nil) if there is a message available.
	// Returns (false, nil) if iteration is done (no more messages).
	// Returns (false, error) if an error occurred (e.g., service disconnected, context cancelled).
	// Must be called before accessing Message().
	Next(ctx context.Context) (bool, error)

	// Message returns the current message with full mutation capabilities.
	// Must be called after a successful Next() call that returned (true, nil).
	// Returns ErrIteratorOutOfBounds if called before Next() or after iteration ends.
	Message() (Message, error)
}

// StreamOptions configures streaming behavior.
type StreamOptions struct {
	// BatchSize is the number of messages fetched per batch.
	// Larger batches reduce round-trips but use more memory.
	// Default: 100
	BatchSize int
}

// messageIterator implements MessageIterator using cursor-based batch fetching.
// Uses StartAfter for proper keyset pagination, avoiding the issues with
// offset-based pagination when data changes between fetches.
type messageIterator struct {
	mailbox  *userMailbox
	storeRef store.Store
	filters  []store.Filter
	opts     store.ListOptions
	batch    []store.Message
	batchIdx int
	done     bool
	fetched  bool
}

// newMessageIterator creates a new iterator for the given filters.
func newMessageIterator(mailbox *userMailbox, filters []store.Filter, streamOpts StreamOptions) *messageIterator {
	batchSize := streamOpts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}

	return &messageIterator{
		mailbox:  mailbox,
		storeRef: mailbox.service.store,
		filters:  filters,
		opts: store.ListOptions{
			Limit:     batchSize,
			SortBy:    "created_at",
			SortOrder: store.SortDesc,
		},
	}
}

func (it *messageIterator) Next(ctx context.Context) (bool, error) {
	if it.done {
		return false, nil
	}

	// Verify service is still connected on each iteration
	if err := it.mailbox.checkAccess(); err != nil {
		it.done = true
		return false, err
	}

	// Check if we need to fetch next batch
	if it.batchIdx >= len(it.batch) {
		// Check if we've exhausted all results
		if it.fetched && len(it.batch) < it.opts.Limit {
			it.done = true
			return false, nil
		}

		// Fetch next batch
		list, err := it.storeRef.Find(ctx, it.filters, it.opts)
		if err != nil {
			it.done = true
			return false, err
		}

		it.batch = list.Messages
		it.batchIdx = 0
		it.fetched = true

		// Set cursor for next batch using last message ID
		// The store handles keyset pagination via StartAfter
		if len(it.batch) > 0 {
			it.opts.StartAfter = it.batch[len(it.batch)-1].GetID()
		}

		// Check if this batch is empty
		if len(it.batch) == 0 {
			it.done = true
			return false, nil
		}
	}

	it.batchIdx++
	return true, nil
}

func (it *messageIterator) Message() (Message, error) {
	if it.batchIdx <= 0 || it.batchIdx > len(it.batch) {
		return nil, ErrIteratorOutOfBounds
	}
	return newMessage(it.batch[it.batchIdx-1], it.mailbox), nil
}

// StreamInbox returns an iterator for inbox messages.
func (m *userMailbox) StreamInbox(ctx context.Context, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.InFolder(store.FolderInbox),
		store.NotDeleted(),
	}
	return newMessageIterator(m, filters, opts), nil
}

// StreamSent returns an iterator for sent messages.
func (m *userMailbox) StreamSent(ctx context.Context, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.InFolder(store.FolderSent),
		store.NotDeleted(),
	}
	return newMessageIterator(m, filters, opts), nil
}

// StreamFolder returns an iterator for messages in a specific folder.
func (m *userMailbox) StreamFolder(ctx context.Context, folderID string, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.InFolder(folderID),
		store.NotDeleted(),
	}
	return newMessageIterator(m, filters, opts), nil
}

// StreamArchived returns an iterator for archived messages.
func (m *userMailbox) StreamArchived(ctx context.Context, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.InFolder(store.FolderArchived),
		store.NotDeleted(),
	}
	return newMessageIterator(m, filters, opts), nil
}

// StreamTrash returns an iterator for trashed messages.
func (m *userMailbox) StreamTrash(ctx context.Context, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.InFolder(store.FolderTrash),
	}
	return newMessageIterator(m, filters, opts), nil
}

// StreamByTag returns an iterator for messages with a specific tag.
func (m *userMailbox) StreamByTag(ctx context.Context, tagID string, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.HasTag(tagID),
		store.NotDeleted(),
	}
	return newMessageIterator(m, filters, opts), nil
}

// StreamUnread returns an iterator for unread messages.
func (m *userMailbox) StreamUnread(ctx context.Context, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.IsReadFilter(false),
		store.NotDeleted(),
	}
	return newMessageIterator(m, filters, opts), nil
}

// StreamSearch returns an iterator for search results.
func (m *userMailbox) StreamSearch(ctx context.Context, query string, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	return &searchIterator{
		mailbox:  m,
		storeRef: m.service.store,
		query: store.SearchQuery{
			Query:   query,
			OwnerID: m.userID,
			Filters: []store.Filter{
				store.OwnerIs(m.userID),
				store.NotDeleted(),
			},
			Options: store.ListOptions{
				Limit:     batchSize,
				SortBy:    "created_at",
				SortOrder: store.SortDesc,
			},
		},
		batchSize: batchSize,
	}, nil
}

// searchIterator is specialized for search queries using cursor-based pagination.
type searchIterator struct {
	mailbox   *userMailbox
	storeRef  store.Store
	query     store.SearchQuery
	batch     []store.Message
	batchIdx  int
	done      bool
	fetched   bool
	batchSize int
}

func (it *searchIterator) Next(ctx context.Context) (bool, error) {
	if it.done {
		return false, nil
	}

	// Verify service is still connected on each iteration
	if err := it.mailbox.checkAccess(); err != nil {
		it.done = true
		return false, err
	}

	// Check if we need to fetch next batch
	if it.batchIdx >= len(it.batch) {
		if it.fetched && len(it.batch) < it.batchSize {
			it.done = true
			return false, nil
		}

		list, err := it.storeRef.Search(ctx, it.query)
		if err != nil {
			it.done = true
			return false, err
		}

		it.batch = list.Messages
		it.batchIdx = 0
		it.fetched = true

		// Set cursor for next batch
		if len(it.batch) > 0 {
			it.query.Options.StartAfter = it.batch[len(it.batch)-1].GetID()
		}

		if len(it.batch) == 0 {
			it.done = true
			return false, nil
		}
	}

	it.batchIdx++
	return true, nil
}

func (it *searchIterator) Message() (Message, error) {
	if it.batchIdx <= 0 || it.batchIdx > len(it.batch) {
		return nil, ErrIteratorOutOfBounds
	}
	return newMessage(it.batch[it.batchIdx-1], it.mailbox), nil
}
