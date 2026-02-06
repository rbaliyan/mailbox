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
//	iter, _ := mb.Stream(ctx, nil, StreamOptions{BatchSize: 100})
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
//	iter, _ := mb.Stream(ctx, nil, StreamOptions{BatchSize: 100})
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

// batchFetchFunc fetches the next batch of messages.
type batchFetchFunc func(ctx context.Context) ([]store.Message, error)

// batchIterator provides shared cursor-based batch fetching logic.
// Uses StartAfter for proper keyset pagination, avoiding the issues with
// offset-based pagination when data changes between fetches.
type batchIterator struct {
	mailbox   *userMailbox
	fetch     batchFetchFunc
	setCursor func(lastID string)
	batchSize int
	batch     []store.Message
	batchIdx  int
	done      bool
	fetched   bool
}

func (it *batchIterator) Next(ctx context.Context) (bool, error) {
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
		if it.fetched && len(it.batch) < it.batchSize {
			it.done = true
			return false, nil
		}

		// Fetch next batch
		messages, err := it.fetch(ctx)
		if err != nil {
			it.done = true
			return false, err
		}

		it.batch = messages
		it.batchIdx = 0
		it.fetched = true

		// Set cursor for next batch using last message ID
		if len(it.batch) > 0 {
			it.setCursor(it.batch[len(it.batch)-1].GetID())
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

func (it *batchIterator) Message() (Message, error) {
	if it.batchIdx <= 0 || it.batchIdx > len(it.batch) {
		return nil, ErrIteratorOutOfBounds
	}
	return newMessage(it.batch[it.batchIdx-1], it.mailbox), nil
}

// messageIterator implements MessageIterator for filtered queries.
type messageIterator struct {
	batchIterator
	storeRef store.Store
	filters  []store.Filter
	opts     store.ListOptions
}

// newMessageIterator creates a new iterator for the given filters.
func newMessageIterator(mailbox *userMailbox, filters []store.Filter, streamOpts StreamOptions) *messageIterator {
	batchSize := streamOpts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}

	it := &messageIterator{
		storeRef: mailbox.service.store,
		filters:  filters,
		opts: store.ListOptions{
			Limit:     batchSize,
			SortBy:    "created_at",
			SortOrder: store.SortDesc,
		},
	}
	it.mailbox = mailbox
	it.batchSize = batchSize
	it.fetch = func(ctx context.Context) ([]store.Message, error) {
		list, err := it.storeRef.Find(ctx, it.filters, it.opts)
		if err != nil {
			return nil, err
		}
		return list.Messages, nil
	}
	it.setCursor = func(lastID string) {
		it.opts.StartAfter = lastID
	}
	return it
}

// Stream returns an iterator for messages matching the given filters.
// The owner filter and not-deleted filter are automatically prepended.
// Pass additional filters to narrow results (e.g., store.InFolder, store.HasTag).
func (m *userMailbox) Stream(ctx context.Context, filters []store.Filter, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	base := []store.Filter{
		store.OwnerIs(m.userID),
		store.NotDeleted(),
	}
	return newMessageIterator(m, append(base, filters...), opts), nil
}

// searchIterator implements MessageIterator for search queries.
type searchIterator struct {
	batchIterator
	storeRef store.Store
	query    store.SearchQuery
}

// StreamSearch returns an iterator for search results.
func (m *userMailbox) StreamSearch(ctx context.Context, query SearchQuery, opts StreamOptions) (MessageIterator, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	// Ensure owner and not-deleted filters are set.
	baseFilters := []store.Filter{
		store.OwnerIs(m.userID),
		store.NotDeleted(),
	}

	it := &searchIterator{
		storeRef: m.service.store,
		query: store.SearchQuery{
			Query:   query.Query,
			OwnerID: m.userID,
			Fields:  query.Fields,
			Tags:    query.Tags,
			Filters: append(baseFilters, query.Filters...),
			Options: store.ListOptions{
				Limit:     batchSize,
				SortBy:    "created_at",
				SortOrder: store.SortDesc,
			},
		},
	}
	it.mailbox = m
	it.batchSize = batchSize
	it.fetch = func(ctx context.Context) ([]store.Message, error) {
		list, err := it.storeRef.Search(ctx, it.query)
		if err != nil {
			return nil, err
		}
		return list.Messages, nil
	}
	it.setCursor = func(lastID string) {
		it.query.Options.StartAfter = lastID
	}
	return it, nil
}
