package mailbox

import (
	"context"
	"fmt"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"go.opentelemetry.io/otel/attribute"
)

// Get retrieves a message by ID.
func (m *userMailbox) Get(ctx context.Context, messageID string) (Message, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// OTel tracing
	ctx, endSpan := m.service.otel.startSpan(ctx, "mailbox.get",
		attribute.String("user_id", m.userID),
		attribute.String("message_id", messageID),
	)
	start := time.Now()
	var getErr error
	defer func() {
		endSpan(getErr)
		m.service.otel.recordGet(ctx, time.Since(start), getErr)
	}()

	msg, storeErr := m.service.store.Get(ctx, messageID)
	if storeErr != nil {
		getErr = storeErr
		return nil, fmt.Errorf("get message: %w", storeErr)
	}

	if !m.canAccess(msg) {
		getErr = ErrUnauthorized
		return nil, ErrUnauthorized
	}

	return newMessage(msg, m), nil
}

// Inbox returns messages in the user's inbox (received messages, not archived or trashed).
func (m *userMailbox) Inbox(ctx context.Context, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "inbox", opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(store.FolderInbox),
			store.NotDeleted(),
		}
	})
}

// Sent returns messages sent by the user.
func (m *userMailbox) Sent(ctx context.Context, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "sent", opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(store.FolderSent),
			store.NotDeleted(),
		}
	})
}

// Archived returns archived messages for the current user.
func (m *userMailbox) Archived(ctx context.Context, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "archived", opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(store.FolderArchived),
			store.NotDeleted(),
		}
	})
}

// Trash returns messages in trash for the current user.
func (m *userMailbox) Trash(ctx context.Context, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "trash", opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(store.FolderTrash),
		}
	})
}

// Drafts returns draft messages for the current user.
func (m *userMailbox) Drafts(ctx context.Context, opts store.ListOptions) (DraftList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	storeDrafts, err := m.service.store.ListDrafts(ctx, m.userID, opts)
	if err != nil {
		return nil, fmt.Errorf("list drafts: %w", err)
	}

	drafts := make([]Draft, len(storeDrafts.Drafts))
	for i, d := range storeDrafts.Drafts {
		drafts[i] = &draft{
			mailbox: m,
			message: d,
			saved:   true,
		}
	}

	return &draftList{
		mailbox:    m,
		drafts:     drafts,
		total:      storeDrafts.Total,
		hasMore:    storeDrafts.HasMore,
		nextCursor: storeDrafts.NextCursor,
	}, nil
}

// Folder returns messages in a specific folder for the current user.
func (m *userMailbox) Folder(ctx context.Context, folderID string, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "folder:"+folderID, opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(folderID),
			store.NotDeleted(),
		}
	})
}

// listWithOTel is a helper that adds OTel instrumentation to list operations.
func (m *userMailbox) listWithOTel(ctx context.Context, folder string, opts store.ListOptions, getFilters func() []store.Filter) (MessageList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// OTel tracing
	ctx, endSpan := m.service.otel.startSpan(ctx, "mailbox.list",
		attribute.String("user_id", m.userID),
		attribute.String("folder", folder),
	)
	start := time.Now()
	var listErr error
	var resultCount int
	defer func() {
		endSpan(listErr)
		m.service.otel.recordList(ctx, time.Since(start), folder, resultCount, listErr)
	}()

	filters := getFilters()
	storeList, err := m.listMessages(ctx, filters, opts)
	if err != nil {
		listErr = err
		return nil, err
	}
	resultCount = len(storeList.Messages)

	return wrapMessageList(storeList, m), nil
}

// Search searches messages accessible to the current user.
func (m *userMailbox) Search(ctx context.Context, query SearchQuery) (MessageList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// OTel tracing
	ctx, endSpan := m.service.otel.startSpan(ctx, "mailbox.search",
		attribute.String("user_id", m.userID),
		attribute.String("query", query.Query),
	)
	start := time.Now()
	var searchErr error
	var resultCount int
	defer func() {
		endSpan(searchErr)
		m.service.otel.recordSearch(ctx, time.Since(start), resultCount, searchErr)
	}()

	// Apply default query limit if not specified
	if query.Options.Limit == 0 {
		query.Options.Limit = m.service.opts.defaultQueryLimit
	}
	// Enforce maximum query limit to prevent resource exhaustion
	if query.Options.Limit > m.service.opts.maxQueryLimit {
		query.Options.Limit = m.service.opts.maxQueryLimit
	}

	// Set owner for search
	query.OwnerID = m.userID

	// Add filter to only search user's messages
	query.Filters = append(query.Filters,
		store.OwnerIs(m.userID),
		store.NotDeleted(),
	)

	// Add tag filters if specified
	if len(query.Tags) > 0 {
		query.Filters = append(query.Filters, store.HasTags(query.Tags)...)
	}

	storeList, err := m.service.store.Search(ctx, query)
	if err != nil {
		searchErr = err
		return nil, fmt.Errorf("search messages: %w", err)
	}

	resultCount = len(storeList.Messages)
	return wrapMessageList(storeList, m), nil
}

// systemFolders defines the standard system folders with their display names.
var systemFolders = []struct {
	ID   string
	Name string
}{
	{store.FolderInbox, "Inbox"},
	{store.FolderSent, "Sent"},
	{store.FolderArchived, "Archived"},
	{store.FolderTrash, "Trash"},
}

// systemFolderSet is used to quickly check if a folder ID is a system folder.
var systemFolderSet = func() map[string]bool {
	m := make(map[string]bool, len(systemFolders))
	for _, sf := range systemFolders {
		m[sf.ID] = true
	}
	return m
}()

// ListFolders returns information about all folders for the current user.
// Always includes system folders. If the store implements store.FolderLister,
// custom (non-system) folders are included as well.
// If the store implements store.FolderCounter, batch counting is used.
func (m *userMailbox) ListFolders(ctx context.Context) ([]FolderInfo, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// Discover all folder IDs (system + custom if store supports it).
	allFolderIDs := make([]string, len(systemFolders))
	for i, sf := range systemFolders {
		allFolderIDs[i] = sf.ID
	}

	var customFolderIDs []string
	if fl, ok := m.service.store.(store.FolderLister); ok {
		distinct, err := fl.ListDistinctFolders(ctx, m.userID)
		if err != nil {
			return nil, fmt.Errorf("list distinct folders: %w", err)
		}
		for _, id := range distinct {
			if !systemFolderSet[id] {
				customFolderIDs = append(customFolderIDs, id)
				allFolderIDs = append(allFolderIDs, id)
			}
		}
	}

	// Fast path: use batch counting if the store supports it.
	if fc, ok := m.service.store.(store.FolderCounter); ok {
		counts, err := fc.CountByFolders(ctx, m.userID, allFolderIDs)
		if err != nil {
			return nil, fmt.Errorf("count folders: %w", err)
		}

		folders := make([]FolderInfo, 0, len(allFolderIDs))
		for _, sf := range systemFolders {
			c := counts[sf.ID]
			folders = append(folders, FolderInfo{
				ID:           sf.ID,
				Name:         sf.Name,
				IsSystem:     true,
				MessageCount: c.Total,
				UnreadCount:  c.Unread,
			})
		}
		for _, id := range customFolderIDs {
			c := counts[id]
			folders = append(folders, FolderInfo{
				ID:           id,
				Name:         id,
				MessageCount: c.Total,
				UnreadCount:  c.Unread,
			})
		}
		return folders, nil
	}

	// Slow path: individual Count queries per folder.
	folders := make([]FolderInfo, 0, len(allFolderIDs))

	for _, id := range allFolderIDs {
		count, err := m.service.store.Count(ctx, []store.Filter{
			store.OwnerIs(m.userID),
			store.NotDeleted(),
			store.InFolder(id),
		})
		if err != nil {
			return nil, fmt.Errorf("count folder %s: %w", id, err)
		}

		unreadCount, err := m.service.store.Count(ctx, []store.Filter{
			store.OwnerIs(m.userID),
			store.NotDeleted(),
			store.InFolder(id),
			store.IsReadFilter(false),
		})
		if err != nil {
			return nil, fmt.Errorf("count unread in folder %s: %w", id, err)
		}

		isSystem := systemFolderSet[id]
		name := id
		if isSystem {
			for _, sf := range systemFolders {
				if sf.ID == id {
					name = sf.Name
					break
				}
			}
		}

		folders = append(folders, FolderInfo{
			ID:           id,
			Name:         name,
			IsSystem:     isSystem,
			MessageCount: count,
			UnreadCount:  unreadCount,
		})
	}

	return folders, nil
}

// GetThread returns all messages in a thread, ordered by creation time.
// Only returns messages owned by the current user.
func (m *userMailbox) GetThread(ctx context.Context, threadID string, opts store.ListOptions) (MessageList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	if threadID == "" {
		return nil, fmt.Errorf("%w: empty thread ID", ErrInvalidID)
	}

	// Set default sorting to creation time ascending for threads
	if opts.SortBy == "" {
		opts.SortBy = "created_at"
		opts.SortOrder = store.SortAsc
	}

	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.NotDeleted(),
		store.ThreadIs(threadID),
	}

	storeList, err := m.service.store.Find(ctx, filters, opts)
	if err != nil {
		return nil, fmt.Errorf("get thread: %w", err)
	}

	return wrapMessageList(storeList, m), nil
}

// GetReplies returns all direct replies to a message.
// Only returns replies owned by the current user.
func (m *userMailbox) GetReplies(ctx context.Context, messageID string, opts store.ListOptions) (MessageList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	if messageID == "" {
		return nil, fmt.Errorf("%w: empty message ID", ErrInvalidID)
	}

	// Set default sorting to creation time ascending
	if opts.SortBy == "" {
		opts.SortBy = "created_at"
		opts.SortOrder = store.SortAsc
	}

	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.NotDeleted(),
		store.ReplyToIs(messageID),
	}

	storeList, err := m.service.store.Find(ctx, filters, opts)
	if err != nil {
		return nil, fmt.Errorf("get replies: %w", err)
	}

	return wrapMessageList(storeList, m), nil
}

// listMessages is a shared helper for listing messages with query limit enforcement
// and optional FindWithCount optimization.
func (m *userMailbox) listMessages(ctx context.Context, filters []store.Filter, opts store.ListOptions) (*store.MessageList, error) {
	// Apply default query limit if not specified
	if opts.Limit == 0 {
		opts.Limit = m.service.opts.defaultQueryLimit
	}
	// Enforce maximum query limit to prevent resource exhaustion
	if opts.Limit > m.service.opts.maxQueryLimit {
		opts.Limit = m.service.opts.maxQueryLimit
	}
	if opts.SortBy == "" {
		opts.SortBy = "CreatedAt"
		opts.SortOrder = store.SortDesc
	}

	// Fast path: use combined find+count if the store supports it.
	var list *store.MessageList
	var total int64
	if fwc, ok := m.service.store.(store.FindWithCounter); ok {
		var err error
		list, total, err = fwc.FindWithCount(ctx, filters, opts)
		if err != nil {
			return nil, fmt.Errorf("find messages: %w", err)
		}
	} else {
		var err error
		list, err = m.service.store.Find(ctx, filters, opts)
		if err != nil {
			return nil, fmt.Errorf("find messages: %w", err)
		}
		total, err = m.service.store.Count(ctx, filters)
		if err != nil {
			return nil, fmt.Errorf("count messages: %w", err)
		}
	}

	var nextCursor string
	if list.HasMore && len(list.Messages) > 0 {
		nextCursor = list.Messages[len(list.Messages)-1].GetID()
	}

	return &store.MessageList{
		Messages:   list.Messages,
		Total:      total,
		HasMore:    list.HasMore,
		NextCursor: nextCursor,
	}, nil
}
