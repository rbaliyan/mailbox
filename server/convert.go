package server

import (
	"github.com/rbaliyan/mailbox"
	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/store"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// toProtoMessage converts a mailbox.Message to its proto representation.
// Returns nil when msg is nil. Logs a warning via the server logger when
// metadata cannot be serialised to proto (non-primitive values).
func (s *Server) toProtoMessage(msg mailbox.Message) *mailboxpb.Message {
	if msg == nil {
		return nil
	}
	m := &mailboxpb.Message{
		Id:           msg.GetID(),
		OwnerId:      msg.GetOwnerID(),
		SenderId:     msg.GetSenderID(),
		Subject:      msg.GetSubject(),
		Body:         msg.GetBody(),
		RecipientIds: msg.GetRecipientIDs(),
		Headers:      msg.GetHeaders(),
		Status:       string(msg.GetStatus()),
		IsRead:       msg.GetIsRead(),
		FolderId:     msg.GetFolderID(),
		Tags:         msg.GetTags(),
		ThreadId:     msg.GetThreadID(),
		ReplyToId:    msg.GetReplyToID(),
		CreatedAt:    timestamppb.New(msg.GetCreatedAt()),
		UpdatedAt:    timestamppb.New(msg.GetUpdatedAt()),
	}
	if meta := msg.GetMetadata(); len(meta) > 0 {
		if st, err := structpb.NewStruct(meta); err == nil {
			m.Metadata = st
		} else {
			s.opts.logger.Warn("message metadata could not be serialised to proto",
				"message_id", msg.GetID(), "error", err)
		}
	}
	if t := msg.GetExpiresAt(); t != nil {
		m.ExpiresAt = timestamppb.New(*t)
	}
	if t := msg.GetAvailableAt(); t != nil {
		m.AvailableAt = timestamppb.New(*t)
	}
	if msg.GetIsRead() {
		if t := msg.GetReadAt(); t != nil {
			m.ReadAt = timestamppb.New(*t)
		}
	}
	for _, a := range msg.GetAttachments() {
		m.Attachments = append(m.Attachments, toProtoAttachment(a))
	}
	return m
}

// toProtoDraft converts a mailbox.Draft to its proto representation.
// Returns nil when d is nil. Logs a warning via the server logger when
// metadata cannot be serialised to proto (non-primitive values).
func (s *Server) toProtoDraft(d mailbox.Draft) *mailboxpb.Draft {
	if d == nil {
		return nil
	}
	pb := &mailboxpb.Draft{
		Id:           d.ID(),
		OwnerId:      d.OwnerID(),
		Subject:      d.Subject(),
		Body:         d.Body(),
		RecipientIds: d.RecipientIDs(),
		DeliverTo:    d.DeliverTo(),
		Headers:      d.Headers(),
		ThreadId:     d.ThreadID(),
		ReplyToId:    d.ReplyToID(),
	}
	if meta := d.Metadata(); len(meta) > 0 {
		if st, err := structpb.NewStruct(meta); err == nil {
			pb.Metadata = st
		} else {
			s.opts.logger.Warn("draft metadata could not be serialised to proto",
				"draft_id", d.ID(), "error", err)
		}
	}
	for _, a := range d.Attachments() {
		pb.Attachments = append(pb.Attachments, toProtoAttachment(a))
	}
	return pb
}

// toProtoMessageList converts a mailbox.MessageList to a MessageListResponse.
func (s *Server) toProtoMessageList(list mailbox.MessageList) *mailboxpb.MessageListResponse {
	resp := &mailboxpb.MessageListResponse{
		Total:      list.Total(),
		HasMore:    list.HasMore(),
		NextCursor: list.NextCursor(),
	}
	for _, msg := range list.All() {
		resp.Messages = append(resp.Messages, s.toProtoMessage(msg))
	}
	return resp
}

// toProtoAttachment converts a store.Attachment to its proto representation.
func toProtoAttachment(a store.Attachment) *mailboxpb.Attachment {
	if a == nil {
		return nil
	}
	return &mailboxpb.Attachment{
		Id:          a.GetID(),
		Filename:    a.GetFilename(),
		ContentType: a.GetContentType(),
		Size:        a.GetSize(),
		Uri:         a.GetURI(),
		CreatedAt:   timestamppb.New(a.GetCreatedAt()),
	}
}

// toProtoStats converts a store.MailboxStats to its proto representation.
func toProtoStats(s *store.MailboxStats) *mailboxpb.MailboxStats {
	if s == nil {
		return &mailboxpb.MailboxStats{}
	}
	pb := &mailboxpb.MailboxStats{
		TotalMessages: s.TotalMessages,
		UnreadCount:   s.UnreadCount,
		DraftCount:    s.DraftCount,
	}
	if len(s.Folders) > 0 {
		pb.Folders = make(map[string]*mailboxpb.FolderCounts, len(s.Folders))
		for id, fc := range s.Folders {
			pb.Folders[id] = &mailboxpb.FolderCounts{Total: fc.Total, Unread: fc.Unread}
		}
	}
	return pb
}

// toProtoFolderInfo converts a mailbox.FolderInfo to its proto representation.
func toProtoFolderInfo(fi mailbox.FolderInfo) *mailboxpb.FolderInfo {
	return &mailboxpb.FolderInfo{
		Id:           fi.ID,
		Name:         fi.Name,
		IsSystem:     fi.IsSystem,
		MessageCount: fi.MessageCount,
		UnreadCount:  fi.UnreadCount,
	}
}

// toProtoBulkResult converts a *mailbox.BulkResult to a BulkResultResponse.
func toProtoBulkResult(r *mailbox.BulkResult) *mailboxpb.BulkResultResponse {
	resp := &mailboxpb.BulkResultResponse{}
	if r == nil {
		return resp
	}
	resp.SuccessCount = int32(r.SuccessCount()) // #nosec G115 -- bounded by maxBulkSize
	resp.FailureCount = int32(r.FailureCount()) // #nosec G115 -- bounded by maxBulkSize
	for _, op := range r.Results {
		result := &mailboxpb.BulkOperationResult{Id: op.ID, Success: op.Success}
		if op.Error != nil {
			result.Error = safeErrorMessage(op.Error)
		}
		resp.Results = append(resp.Results, result)
	}
	return resp
}

// safeErrorMessage maps err through toGRPCError and returns the stable
// client-safe message string, never forwarding internal error details.
func safeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	st, _ := status.FromError(toGRPCError(err))
	return st.Message()
}

// fromProtoListOptions converts a proto ListOptions to store.ListOptions.
func fromProtoListOptions(opts *mailboxpb.ListOptions) store.ListOptions {
	if opts == nil {
		return store.ListOptions{}
	}
	sortOrder := store.SortAsc
	if opts.SortOrder == "desc" {
		sortOrder = store.SortDesc
	}
	return store.ListOptions{
		Limit:      int(opts.Limit),
		Offset:     int(opts.Offset),
		SortBy:     opts.SortBy,
		SortOrder:  sortOrder,
		StartAfter: opts.StartAfter,
	}
}

// fromProtoFlags converts a proto Flags to mailbox.Flags.
// A nil proto Flags returns the zero mailbox.Flags (no changes).
func fromProtoFlags(f *mailboxpb.Flags) mailbox.Flags {
	flags := mailbox.NewFlags()
	if f == nil {
		return flags
	}
	if f.Read != nil {
		v := f.Read.Value
		flags.Read = &v
	}
	if f.Archived != nil {
		v := f.Archived.Value
		flags.Archived = &v
	}
	return flags
}

