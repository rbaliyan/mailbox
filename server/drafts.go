package server

import (
	"context"

	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ComposeDraft creates and saves a new draft.
func (s *Server) ComposeDraft(ctx context.Context, req *mailboxpb.ComposeDraftRequest) (*mailboxpb.DraftResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	d, err := mb.Compose()
	if err != nil {
		return nil, toGRPCError(err)
	}

	// Apply fields from the request using the fluent composer API.
	if len(req.RecipientIds) > 0 {
		d.SetRecipients(req.RecipientIds...)
	}
	if len(req.DeliverTo) > 0 {
		d.SetDeliverTo(req.DeliverTo...)
	}
	if req.Subject != "" {
		d.SetSubject(req.Subject)
	}
	if req.Body != "" {
		d.SetBody(req.Body)
	}
	for k, v := range req.Headers {
		d.SetHeader(k, v)
	}
	if req.Metadata != nil {
		for k, v := range req.Metadata.AsMap() {
			d.SetMetadata(k, v)
		}
	}
	if req.Ttl != nil && req.Ttl.AsDuration() > 0 {
		d.SetTTL(req.Ttl.AsDuration())
	}
	if req.ScheduleAt != nil {
		d.SetScheduleAt(req.ScheduleAt.AsTime())
	}

	// ReplyTo looks up the parent message and inherits its thread ID.
	if req.ReplyToMessageId != "" {
		if err := d.ReplyTo(ctx, req.ReplyToMessageId); err != nil {
			return nil, toGRPCError(err)
		}
	}

	saved, err := d.Save(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mailboxpb.DraftResponse{Draft: s.toProtoDraft(saved)}, nil
}

// UpdateDraft patches an existing draft.
// Non-empty string fields replace the current value; empty strings are ignored.
// Non-empty repeated fields replace the current list; empty slices are ignored.
// Header and metadata entries are merged into the existing maps.
func (s *Server) UpdateDraft(ctx context.Context, req *mailboxpb.UpdateDraftRequest) (*mailboxpb.DraftResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("draft_id", req.DraftId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	d, err := mb.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, toGRPCError(err)
	}

	if req.Subject != "" {
		d.SetSubject(req.Subject)
	}
	if req.Body != "" {
		d.SetBody(req.Body)
	}
	if len(req.RecipientIds) > 0 {
		d.SetRecipients(req.RecipientIds...)
	}
	if len(req.DeliverTo) > 0 {
		d.SetDeliverTo(req.DeliverTo...)
	}
	for k, v := range req.Headers {
		d.SetHeader(k, v)
	}
	if req.Metadata != nil {
		for k, v := range req.Metadata.AsMap() {
			d.SetMetadata(k, v)
		}
	}
	if req.Ttl != nil && req.Ttl.AsDuration() > 0 {
		d.SetTTL(req.Ttl.AsDuration())
	}
	if req.ScheduleAt != nil {
		d.SetScheduleAt(req.ScheduleAt.AsTime())
	}

	saved, err := d.Save(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mailboxpb.DraftResponse{Draft: s.toProtoDraft(saved)}, nil
}

// ListDrafts lists all drafts for a user.
func (s *Server) ListDrafts(ctx context.Context, req *mailboxpb.ListDraftsRequest) (*mailboxpb.DraftListResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	list, err := mb.Drafts(ctx, fromProtoListOptions(req.Options))
	if err != nil {
		return nil, toGRPCError(err)
	}
	resp := &mailboxpb.DraftListResponse{
		Total:      list.Total(),
		HasMore:    list.HasMore(),
		NextCursor: list.NextCursor(),
	}
	for _, d := range list.All() {
		resp.Drafts = append(resp.Drafts, s.toProtoDraft(d))
	}
	return resp, nil
}

// DeleteDraft permanently deletes a draft.
func (s *Server) DeleteDraft(ctx context.Context, req *mailboxpb.DeleteDraftRequest) (*emptypb.Empty, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("draft_id", req.DraftId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionDelete, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	d, err := mb.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, toGRPCError(err)
	}
	if err := d.Delete(ctx); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// SendDraft sends an existing draft.
func (s *Server) SendDraft(ctx context.Context, req *mailboxpb.SendDraftRequest) (*mailboxpb.MessageResponse, error) {
	if err := requireUserID(req.UserId); err != nil {
		return nil, err
	}
	if err := requireField("draft_id", req.DraftId); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, ActionWrite, req.UserId); err != nil {
		return nil, err
	}
	mb := s.svc.Client(req.UserId)
	d, err := mb.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, toGRPCError(err)
	}
	msg, err := d.Send(ctx)
	return s.handleSendResult(msg, err)
}

