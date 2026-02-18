package mailbox

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"go.opentelemetry.io/otel/attribute"
)

// Compose starts a new message draft.
func (m *userMailbox) Compose() (Draft, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	return newDraft(m), nil
}

// createSenderMessage creates the sender's copy in the outbox.
// Returns the created message or an error. Handles attachment ref counting and rollback.
func (m *userMailbox) createSenderMessage(ctx context.Context, draft store.DraftMessage, threadID, replyToID string) (store.Message, error) {
	senderData := store.MessageData{
		OwnerID:      m.userID,
		SenderID:     m.userID,
		RecipientIDs: draft.GetRecipientIDs(),
		Subject:      draft.GetSubject(),
		Body:         draft.GetBody(),
		Headers:      draft.GetHeaders(),
		Metadata:     draft.GetMetadata(),
		Status:       store.MessageStatusQueued,
		FolderID:     store.FolderOutbox,
		Attachments:  draft.GetAttachments(),
		ThreadID:     threadID,
		ReplyToID:    replyToID,
	}

	senderCopy, err := m.service.store.CreateMessage(ctx, senderData)
	if err != nil {
		return nil, fmt.Errorf("create sender message: %w", err)
	}

	// Increment attachment refs for sender copy.
	// If this fails, we must rollback the sender copy to avoid inconsistent state.
	if err := m.addAttachmentRefs(ctx, senderCopy); err != nil {
		m.service.logger.Error("failed to add attachment refs for sender copy - rolling back",
			"error", err, "message_id", senderCopy.GetID())

		// Try to release any refs that were partially added
		if releaseErr := m.releaseAttachmentRefs(ctx, senderCopy); releaseErr != nil {
			m.service.logger.Error("failed to release partial attachment refs during rollback",
				"error", releaseErr, "message_id", senderCopy.GetID())
		}

		// Rollback: delete the sender copy since attachment refs are inconsistent
		if deleteErr := m.service.store.HardDelete(ctx, senderCopy.GetID()); deleteErr != nil {
			m.service.logger.Error("CRITICAL: failed to rollback sender message after attachment ref failure - orphaned message",
				"error", deleteErr, "message_id", senderCopy.GetID())
			// Return combined error so caller knows rollback failed
			return nil, fmt.Errorf("add attachment refs failed and rollback failed (orphaned message %s): %w",
				senderCopy.GetID(), errors.Join(err, deleteErr))
		}
		return nil, fmt.Errorf("add attachment refs: %w", err)
	}

	return senderCopy, nil
}

// deduplicateRecipients returns a list of unique recipient IDs.
func deduplicateRecipients(recipientIDs []string) []string {
	seen := make(map[string]bool, len(recipientIDs))
	unique := make([]string, 0, len(recipientIDs))
	for _, id := range recipientIDs {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	return unique
}

// deliverToRecipients creates message copies for all unique recipients.
// senderMsgID is used as the idempotency base to prevent duplicates on retry.
// Returns lists of successful and failed recipients.
func (m *userMailbox) deliverToRecipients(ctx context.Context, draft store.DraftMessage, threadID, replyToID, senderMsgID string) ([]string, map[string]error) {
	// Recipients are already deduplicated in sendDraft before validation.
	// Use sender message ID as idempotency base.
	// This ensures retries after partial delivery don't create duplicates,
	// while allowing multiple sends with the same content to create separate messages.
	idempotencyBase := senderMsgID

	var deliveredTo []string
	failedRecipients := make(map[string]error)

	// Create message for each recipient with idempotency
	for i, recipientID := range draft.GetRecipientIDs() {
		if err := ctx.Err(); err != nil {
			for _, remaining := range draft.GetRecipientIDs()[i:] {
				failedRecipients[remaining] = err
			}
			break
		}

		data := store.MessageData{
			OwnerID:      recipientID,
			SenderID:     m.userID,
			RecipientIDs: draft.GetRecipientIDs(), // Keep original list for display
			Subject:      draft.GetSubject(),
			Body:         draft.GetBody(),
			Headers:      draft.GetHeaders(),
			Metadata:     draft.GetMetadata(),
			Status:       store.MessageStatusDelivered,
			FolderID:     store.FolderInbox,
			Attachments:  draft.GetAttachments(),
			ThreadID:     threadID,
			ReplyToID:    replyToID,
		}

		// Use idempotent create to handle retries after partial delivery
		idempotencyKey := fmt.Sprintf("%s:%s", idempotencyBase, recipientID)
		recipientCopy, created, err := m.service.store.CreateMessageIdempotent(ctx, data, idempotencyKey)
		if err != nil {
			failedRecipients[recipientID] = fmt.Errorf("create message: %w", err)
			continue
		}

		// Only add attachment refs for newly created messages
		if created {
			if refErr := m.addAttachmentRefs(ctx, recipientCopy); refErr != nil {
				// Attachment ref failure means the message won't be fully functional.
				// Release any partial refs, then delete the message to maintain consistency.
				if releaseErr := m.releaseAttachmentRefs(ctx, recipientCopy); releaseErr != nil {
					m.service.logger.Error("failed to release partial attachment refs during rollback",
						"error", releaseErr, "message_id", recipientCopy.GetID(), "recipient", recipientID)
				}
				if deleteErr := m.service.store.HardDelete(ctx, recipientCopy.GetID()); deleteErr != nil {
					m.service.logger.Error("failed to rollback recipient message after attachment ref failure",
						"error", deleteErr, "message_id", recipientCopy.GetID(), "recipient", recipientID)
				}
				failedRecipients[recipientID] = fmt.Errorf("add attachment refs: %w", refErr)
				continue
			}
		}

		deliveredTo = append(deliveredTo, recipientID)

		// Publish per-recipient received event (best-effort, don't fail delivery)
		if pubErr := m.service.events.MessageReceived.Publish(ctx, MessageReceivedEvent{
			MessageID:   recipientCopy.GetID(),
			RecipientID: recipientID,
			SenderID:    m.userID,
			Subject:     draft.GetSubject(),
			ReceivedAt:  time.Now().UTC(),
		}); pubErr != nil {
			m.service.opts.safeEventPublishFailure("MessageReceived", pubErr)
		}
	}

	return deliveredTo, failedRecipients
}

// rollbackSenderMessage cleans up the sender's message copy on total delivery failure.
// Returns an error if rollback fails (caller should log but continue).
func (m *userMailbox) rollbackSenderMessage(ctx context.Context, senderCopy store.Message) error {
	var errs []error

	if err := m.releaseAttachmentRefs(ctx, senderCopy); err != nil {
		errs = append(errs, fmt.Errorf("release attachment refs: %w", err))
	}

	if err := m.service.store.HardDelete(ctx, senderCopy.GetID()); err != nil {
		errs = append(errs, fmt.Errorf("hard delete sender message: %w", err))
	}

	return errors.Join(errs...)
}

// finalizeDelivery handles post-delivery tasks: move to sent, draft cleanup, event publishing.
// Returns the message and any error. The message is always returned when possible (even on
// non-event errors) since the message was already created and delivered to recipients.
// If event publishing fails with eventErrorsFatal=true, returns both the message AND an
// EventPublishError so the caller knows the message was sent.
func (m *userMailbox) finalizeDelivery(ctx context.Context, senderCopy store.Message, deliveredTo []string, draft store.DraftMessage, sentAt time.Time) (store.Message, error) {
	// Move sender's copy to sent folder
	if err := m.service.store.MoveToFolder(ctx, senderCopy.GetID(), store.FolderSent); err != nil {
		// Message was created and delivered but folder move failed.
		// Return the sender copy so the caller still has a handle to it.
		return senderCopy, fmt.Errorf("move message to sent folder: %w", err)
	}

	// Delete the draft if it was saved
	if draft.GetID() != "" {
		if err := m.service.store.DeleteDraft(ctx, draft.GetID()); err != nil {
			// Message was sent successfully - log the draft cleanup failure
			// but don't fail the operation. Orphaned draft is minor.
			m.service.logger.Warn("failed to delete draft after send",
				"error", err, "draft_id", draft.GetID())
		}
	}

	// Re-fetch to get updated folder
	updatedCopy, err := m.service.store.Get(ctx, senderCopy.GetID())
	if err != nil {
		// Message was sent but re-fetch failed. Return original sender copy.
		return senderCopy, fmt.Errorf("fetch updated sender copy: %w", err)
	}

	// Publish event - do this AFTER we have the updated copy so we can return it even on error
	if err := m.service.events.MessageSent.Publish(ctx, MessageSentEvent{
		MessageID:    senderCopy.GetID(),
		SenderID:     senderCopy.GetSenderID(),
		RecipientIDs: deliveredTo,
		Subject:      senderCopy.GetSubject(),
		SentAt:       sentAt,
	}); err != nil {
		if m.service.opts.eventErrorsFatal {
			// Return the message WITH an error - message was sent but event failed
			return updatedCopy, &EventPublishError{
				Event:     "MessageSent",
				MessageID: updatedCopy.GetID(),
				Err:       err,
			}
		}
		m.service.opts.safeEventPublishFailure("MessageSent", err)
	}

	return updatedCopy, nil
}

// sendDraft sends a draft message to recipients.
// Creates a copy of the message for the sender and each recipient.
// threadID and replyToID are optional thread context for conversation support.
func (m *userMailbox) sendDraft(ctx context.Context, draft store.DraftMessage, threadID, replyToID string) (store.Message, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// Step 1: Deduplicate recipients before validation so that the recipient count
	// check reflects the actual number of unique recipients.
	draft.SetRecipients(deduplicateRecipients(draft.GetRecipientIDs())...)

	// Step 2: Auto-populate Content-Length header if not already set.
	// This must happen before validation so the auto-added header is included in limit checks.
	headers := draft.GetHeaders()
	if headers == nil || headers[store.HeaderContentLength] == "" {
		// Content-Length is byte length (not rune count), matching HTTP semantics.
		draft.SetHeader(store.HeaderContentLength, strconv.Itoa(len(draft.GetBody())))
	}

	// Step 2b: Validate the draft (before acquiring semaphore to avoid wasting slots)
	if err := ValidateDraft(draft, m.service.opts.getLimits()); err != nil {
		return nil, err
	}

	// Step 2c: Fail early if draft has attachments but no attachment manager is configured.
	// Without a manager, attachment refs won't be tracked and attachments may be orphaned
	// or prematurely deleted.
	if len(draft.GetAttachments()) > 0 && m.service.attachments == nil {
		return nil, ErrAttachmentStoreNotConfigured
	}

	// Setup tracing
	ctx, endSpan := m.service.otel.startSpan(ctx, "mailbox.send",
		attribute.String("user_id", m.userID),
		attribute.Int("recipient_count", len(draft.GetRecipientIDs())),
	)
	start := time.Now()
	var sendErr error
	defer func() {
		endSpan(sendErr)
		m.service.otel.recordSend(ctx, time.Since(start), len(draft.GetRecipientIDs()), sendErr)
	}()

	// Step 3: Acquire send semaphore
	if err := m.service.sendSem.Acquire(ctx, 1); err != nil {
		sendErr = err
		return nil, sendErr
	}
	defer m.service.sendSem.Release(1)

	// Step 4: Plugin BeforeSend hook
	if err := m.service.plugins.beforeSend(ctx, m.userID, draft); err != nil {
		sendErr = err
		return nil, sendErr
	}

	// Step 5: Create sender's copy
	senderCopy, err := m.createSenderMessage(ctx, draft, threadID, replyToID)
	if err != nil {
		sendErr = err
		return nil, sendErr
	}

	// Step 6: Deliver to recipients (use sender message ID for idempotency)
	deliveredTo, failedRecipients := m.deliverToRecipients(ctx, draft, threadID, replyToID, senderCopy.GetID())

	// Step 7: Handle total delivery failure
	if len(deliveredTo) == 0 {
		rollbackErr := m.rollbackSenderMessage(ctx, senderCopy)
		sendErr = fmt.Errorf("send failed: all %d recipients failed delivery", len(draft.GetRecipientIDs()))
		if rollbackErr != nil {
			sendErr = fmt.Errorf("%w (rollback also failed: %v)", sendErr, rollbackErr)
		}
		return nil, sendErr
	}

	// Step 8: Finalize successful delivery
	now := time.Now().UTC()
	updatedSenderCopy, eventErr := m.finalizeDelivery(ctx, senderCopy, deliveredTo, draft, now)
	if eventErr != nil {
		sendErr = eventErr
		// Return the updated sender copy (moved to Sent folder) even on event
		// publish failure since the message was already created and delivered.
		return updatedSenderCopy, sendErr
	}

	// Step 9: Plugin AfterSend hook (runs even on partial delivery since message was sent)
	if err := m.service.plugins.afterSend(ctx, m.userID, updatedSenderCopy); err != nil {
		sendErr = err
		return updatedSenderCopy, sendErr
	}

	// Step 10: Handle partial delivery
	if len(failedRecipients) > 0 {
		sendErr = &PartialDeliveryError{
			MessageID:        updatedSenderCopy.GetID(),
			DeliveredTo:      deliveredTo,
			FailedRecipients: failedRecipients,
		}
		return updatedSenderCopy, sendErr
	}

	return updatedSenderCopy, nil
}

// SendMessage sends a message directly without going through the draft flow.
// If AttachmentIDs are provided, they are resolved via ResolveAttachments
// and merged with any Attachments already in the request.
func (m *userMailbox) SendMessage(ctx context.Context, req SendRequest) (Message, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// Resolve attachment IDs if any
	allAttachments := req.Attachments
	if len(req.AttachmentIDs) > 0 {
		resolved, err := m.ResolveAttachments(ctx, req.AttachmentIDs)
		if err != nil {
			return nil, fmt.Errorf("resolve attachments: %w", err)
		}
		allAttachments = append(allAttachments, resolved...)
	}

	// Build a transient draft
	draft := m.service.store.NewDraft(m.userID)
	draft.SetRecipients(req.RecipientIDs...)
	draft.SetSubject(req.Subject)
	draft.SetBody(req.Body)
	for k, v := range req.Headers {
		draft.SetHeader(k, v)
	}
	for k, v := range req.Metadata {
		draft.SetMetadata(k, v)
	}
	for _, a := range allAttachments {
		draft.AddAttachment(a)
	}

	// Send via existing flow — return message even on partial delivery or event error
	msg, err := m.sendDraft(ctx, draft, req.ThreadID, req.ReplyToID)
	if msg != nil {
		return newMessage(msg, m), err
	}
	return nil, err
}

// ResolveAttachments resolves attachment metadata by IDs.
// Returns attachment metadata for each ID in order.
func (m *userMailbox) ResolveAttachments(ctx context.Context, attachmentIDs []string) ([]store.Attachment, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	if m.service.attachments == nil {
		return nil, ErrAttachmentStoreNotConfigured
	}

	attachments := make([]store.Attachment, 0, len(attachmentIDs))
	for _, id := range attachmentIDs {
		meta, err := m.service.attachments.GetMetadata(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve attachment %s: %w", id, err)
		}
		attachments = append(attachments, meta)
	}

	return attachments, nil
}

// saveDraft saves a draft without sending.
func (m *userMailbox) saveDraft(ctx context.Context, draft store.DraftMessage) (store.DraftMessage, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	limits := m.service.opts.getLimits()

	// Validate content if provided (drafts can have empty subject)
	if draft.GetSubject() != "" {
		if err := ValidateSubjectWithLimits(draft.GetSubject(), limits); err != nil {
			return nil, err
		}
	}
	if draft.GetBody() != "" {
		if err := ValidateBodyWithLimits(draft.GetBody(), limits); err != nil {
			return nil, err
		}
	}
	if err := ValidateHeaders(draft.GetHeaders(), limits); err != nil {
		return nil, err
	}
	if err := ValidateMetadataWithLimits(draft.GetMetadata(), limits); err != nil {
		return nil, err
	}
	// Validate attachments
	if err := ValidateAttachments(draft.GetAttachments(), limits); err != nil {
		return nil, err
	}

	savedDraft, err := m.service.store.SaveDraft(ctx, draft)
	if err != nil {
		return nil, fmt.Errorf("save draft: %w", err)
	}

	return savedDraft, nil
}
