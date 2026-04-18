package server

import (
	"context"

	"github.com/rbaliyan/mailbox"
	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/store"
	"google.golang.org/protobuf/types/known/emptypb"
)

// GetMessage retrieves a single message by ID.
func (s *Server) GetMessage(ctx context.Context, req *mailboxpb.GetMessageRequest) (*mailboxpb.MessageResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	msg, err := mb.Get(ctx, req.MessageId)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mailboxpb.MessageResponse{Message: s.toProtoMessage(msg)}, nil
}

// ListMessages lists messages in a folder with pagination.
func (s *Server) ListMessages(ctx context.Context, req *mailboxpb.ListMessagesRequest) (*mailboxpb.MessageListResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("folder_id", req.FolderId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	list, err := mb.Folder(ctx, req.FolderId, fromProtoListOptions(req.Options))
	if err != nil {
		return nil, toGRPCError(err)
	}
	return s.toProtoMessageList(list), nil
}

// SearchMessages performs full-text search across a user's messages.
func (s *Server) SearchMessages(ctx context.Context, req *mailboxpb.SearchMessagesRequest) (*mailboxpb.MessageListResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("query", req.Query); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	query := store.SearchQuery{
		OwnerID: req.UserId,
		Query:   req.Query,
		Fields:  req.Fields,
		Tags:    req.Tags,
		Options: fromProtoListOptions(req.Options),
	}
	mb := s.svc.Client(req.UserId)
	list, err := mb.Search(ctx, query)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return s.toProtoMessageList(list), nil
}

// GetThread returns all messages in a thread.
func (s *Server) GetThread(ctx context.Context, req *mailboxpb.GetThreadRequest) (*mailboxpb.MessageListResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("thread_id", req.ThreadId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	list, err := mb.GetThread(ctx, req.ThreadId, fromProtoListOptions(req.Options))
	if err != nil {
		return nil, toGRPCError(err)
	}
	return s.toProtoMessageList(list), nil
}

// GetReplies returns direct replies to a message.
func (s *Server) GetReplies(ctx context.Context, req *mailboxpb.GetRepliesRequest) (*mailboxpb.MessageListResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	list, err := mb.GetReplies(ctx, req.MessageId, fromProtoListOptions(req.Options))
	if err != nil {
		return nil, toGRPCError(err)
	}
	return s.toProtoMessageList(list), nil
}

// SendMessage sends a message without the draft workflow.
func (s *Server) SendMessage(ctx context.Context, req *mailboxpb.SendMessageRequest) (*mailboxpb.MessageResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	sendReq := mailbox.SendRequest{
		RecipientIDs:  req.RecipientIds,
		Subject:       req.Subject,
		Body:          req.Body,
		Headers:       req.Headers,
		AttachmentIDs: req.AttachmentIds,
		ThreadID:      req.ThreadId,
		ReplyToID:     req.ReplyToId,
		DeliverTo:     req.DeliverTo,
	}
	if req.Metadata != nil {
		sendReq.Metadata = req.Metadata.AsMap()
	}
	if req.Ttl != nil {
		sendReq.TTL = req.Ttl.AsDuration()
	}
	if req.ScheduleAt != nil {
		t := req.ScheduleAt.AsTime()
		sendReq.ScheduleAt = &t
	}
	mb := s.svc.Client(req.UserId)
	msg, err := mb.SendMessage(ctx, sendReq)
	return s.handleSendResult(msg, err)
}

// UpdateFlags changes the read or archived status of a message.
func (s *Server) UpdateFlags(ctx context.Context, req *mailboxpb.UpdateFlagsRequest) (*emptypb.Empty, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	if err := mb.UpdateFlags(ctx, req.MessageId, fromProtoFlags(req.Flags)); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// MoveMessage moves a message to a different folder.
func (s *Server) MoveMessage(ctx context.Context, req *mailboxpb.MoveMessageRequest) (*emptypb.Empty, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := requireField("folder_id", req.FolderId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	if err := mb.MoveToFolder(ctx, req.MessageId, req.FolderId); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// DeleteMessage moves a message to trash.
func (s *Server) DeleteMessage(ctx context.Context, req *mailboxpb.DeleteMessageRequest) (*emptypb.Empty, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionDelete, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	if err := mb.Delete(ctx, req.MessageId); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// RestoreMessage restores a message from trash.
func (s *Server) RestoreMessage(ctx context.Context, req *mailboxpb.RestoreMessageRequest) (*emptypb.Empty, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	if err := mb.Restore(ctx, req.MessageId); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// PermanentlyDeleteMessage hard-deletes a message from trash.
func (s *Server) PermanentlyDeleteMessage(ctx context.Context, req *mailboxpb.PermanentlyDeleteMessageRequest) (*emptypb.Empty, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionAdmin, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	if err := mb.PermanentlyDelete(ctx, req.MessageId); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// AddTag adds a tag to a message.
func (s *Server) AddTag(ctx context.Context, req *mailboxpb.TagRequest) (*emptypb.Empty, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := requireField("tag_id", req.TagId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	if err := mb.AddTag(ctx, req.MessageId, req.TagId); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// RemoveTag removes a tag from a message.
func (s *Server) RemoveTag(ctx context.Context, req *mailboxpb.TagRequest) (*emptypb.Empty, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("message_id", req.MessageId); err != nil {
		return nil, err
	}
	if err := requireField("tag_id", req.TagId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	if err := mb.RemoveTag(ctx, req.MessageId, req.TagId); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// MarkAllRead marks all unread messages in a folder as read.
func (s *Server) MarkAllRead(ctx context.Context, req *mailboxpb.MarkAllReadRequest) (*mailboxpb.CountResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("folder_id", req.FolderId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	count, err := mb.MarkAllRead(ctx, req.FolderId)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mailboxpb.CountResponse{Count: count}, nil
}

// BulkUpdateFlags changes the read/archived status of multiple messages.
func (s *Server) BulkUpdateFlags(ctx context.Context, req *mailboxpb.BulkUpdateFlagsRequest) (*mailboxpb.BulkResultResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	if err := s.requireIDs("message_ids", req.MessageIds); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	result, err := mb.BulkUpdateFlags(ctx, req.MessageIds, fromProtoFlags(req.Flags))
	if err != nil && result == nil {
		return nil, toGRPCError(err)
	}
	return toProtoBulkResult(result), nil
}

// BulkMove moves multiple messages to a folder.
func (s *Server) BulkMove(ctx context.Context, req *mailboxpb.BulkMoveRequest) (*mailboxpb.BulkResultResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	if err := s.requireIDs("message_ids", req.MessageIds); err != nil {
		return nil, err
	}
	if err := requireField("folder_id", req.FolderId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	result, err := mb.BulkMove(ctx, req.MessageIds, req.FolderId)
	if err != nil && result == nil {
		return nil, toGRPCError(err)
	}
	return toProtoBulkResult(result), nil
}

// BulkDelete moves multiple messages to trash.
func (s *Server) BulkDelete(ctx context.Context, req *mailboxpb.BulkDeleteRequest) (*mailboxpb.BulkResultResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionDelete, req.UserId); err != nil {
		return nil, err
	}
	if err := s.requireIDs("message_ids", req.MessageIds); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	result, err := mb.BulkDelete(ctx, req.MessageIds)
	if err != nil && result == nil {
		return nil, toGRPCError(err)
	}
	return toProtoBulkResult(result), nil
}

// BulkPermanentlyDelete hard-deletes multiple messages from trash.
func (s *Server) BulkPermanentlyDelete(ctx context.Context, req *mailboxpb.BulkPermanentlyDeleteRequest) (*mailboxpb.BulkResultResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionAdmin, req.UserId); err != nil {
		return nil, err
	}
	if err := s.requireIDs("message_ids", req.MessageIds); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	result, err := mb.BulkPermanentlyDelete(ctx, req.MessageIds)
	if err != nil && result == nil {
		return nil, toGRPCError(err)
	}
	return toProtoBulkResult(result), nil
}

// BulkAddTag adds a tag to multiple messages.
func (s *Server) BulkAddTag(ctx context.Context, req *mailboxpb.BulkTagRequest) (*mailboxpb.BulkResultResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	if err := s.requireIDs("message_ids", req.MessageIds); err != nil {
		return nil, err
	}
	if err := requireField("tag_id", req.TagId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	result, err := mb.BulkAddTag(ctx, req.MessageIds, req.TagId)
	if err != nil && result == nil {
		return nil, toGRPCError(err)
	}
	return toProtoBulkResult(result), nil
}

// BulkRemoveTag removes a tag from multiple messages.
func (s *Server) BulkRemoveTag(ctx context.Context, req *mailboxpb.BulkTagRequest) (*mailboxpb.BulkResultResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	if err := s.requireIDs("message_ids", req.MessageIds); err != nil {
		return nil, err
	}
	if err := requireField("tag_id", req.TagId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	result, err := mb.BulkRemoveTag(ctx, req.MessageIds, req.TagId)
	if err != nil && result == nil {
		return nil, toGRPCError(err)
	}
	return toProtoBulkResult(result), nil
}

// GetStats returns aggregate statistics for a user's mailbox.
func (s *Server) GetStats(ctx context.Context, req *mailboxpb.GetStatsRequest) (*mailboxpb.StatsResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	stats, err := mb.Stats(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mailboxpb.StatsResponse{Stats: toProtoStats(stats)}, nil
}

// UnreadCount returns the total number of unread messages for a user.
func (s *Server) UnreadCount(ctx context.Context, req *mailboxpb.UnreadCountRequest) (*mailboxpb.CountResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	count, err := mb.UnreadCount(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mailboxpb.CountResponse{Count: count}, nil
}

// ListFolders lists all folders (system and custom) for a user.
func (s *Server) ListFolders(ctx context.Context, req *mailboxpb.ListFoldersRequest) (*mailboxpb.FolderListResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	folders, err := mb.ListFolders(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	resp := &mailboxpb.FolderListResponse{}
	for _, fi := range folders {
		resp.Folders = append(resp.Folders, toProtoFolderInfo(fi))
	}
	return resp, nil
}
