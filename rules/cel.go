package rules

import (
	"github.com/google/cel-go/cel"
	"github.com/rbaliyan/mailbox/store"
)

// newCELEnv creates the shared CEL environment with message variable declarations
// and custom helper functions.
func newCELEnv() (*cel.Env, error) {
	opts := []cel.EnvOption{
		cel.Variable("sender", cel.StringType),
		cel.Variable("subject", cel.StringType),
		cel.Variable("body", cel.StringType),
		cel.Variable("recipients", cel.ListType(cel.StringType)),
		cel.Variable("headers", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("metadata", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("has_attachments", cel.BoolType),
		cel.Variable("attachment_count", cel.IntType),
		cel.Variable("thread_id", cel.StringType),
		cel.Variable("is_reply", cel.BoolType),
		cel.Variable("folder", cel.StringType),
		cel.Variable("tags", cel.ListType(cel.StringType)),
	}
	opts = append(opts, customFunctions()...)
	return cel.NewEnv(opts...)
}

// buildActivation creates a CEL activation map from a store.Message.
func buildActivation(msg store.Message) map[string]any {
	headers := msg.GetHeaders()
	if headers == nil {
		headers = map[string]string{}
	}
	metadata := msg.GetMetadata()
	if metadata == nil {
		metadata = map[string]any{}
	}
	tags := msg.GetTags()
	if tags == nil {
		tags = []string{}
	}
	recipients := msg.GetRecipientIDs()
	if recipients == nil {
		recipients = []string{}
	}
	return map[string]any{
		"sender":           msg.GetSenderID(),
		"subject":          msg.GetSubject(),
		"body":             msg.GetBody(),
		"recipients":       recipients,
		"headers":          headers,
		"metadata":         metadata,
		"has_attachments":  len(msg.GetAttachments()) > 0,
		"attachment_count": int64(len(msg.GetAttachments())),
		"thread_id":        msg.GetThreadID(),
		"is_reply":         msg.GetReplyToID() != "",
		"folder":           msg.GetFolderID(),
		"tags":             tags,
	}
}
