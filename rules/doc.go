// Package rules provides a CEL-based message rules engine for the mailbox library.
//
// Rules evaluate Common Expression Language (CEL) conditions against incoming
// or outgoing messages and apply actions such as moving to a folder, adding tags,
// marking as read, or suppressing delivery.
//
// The engine is designed as a mailbox Plugin (implementing SendHook for send-side
// rules) and an event handler (OnMessageReceived for receive-side rules).
// This keeps the core mailbox library clean and makes rules fully optional.
//
// # Rule Scopes
//
// Rules have two scopes:
//   - ScopeReceive: evaluated when a message is delivered to a recipient.
//     All actions are valid (SetFolder, AddTag, RemoveTag, MarkRead, Delete,
//     HardDelete, Archive, Spam, Webhook, Forward).
//   - ScopeSend: evaluated after the sender's copy is created.
//     Only SetFolder, AddTag, RemoveTag, Archive, and Webhook are valid;
//     other actions are silently skipped.
//
// # CEL Variables
//
// The following variables are available in CEL expressions:
//
//	sender           string              - sender user ID
//	subject          string              - message subject
//	body             string              - message body
//	recipients       list(string)        - recipient user IDs
//	headers          map(string, string) - message headers
//	metadata         map(string, dyn)    - message metadata
//	has_attachments  bool                - whether the message has attachments
//	attachment_count int                 - number of attachments
//	thread_id        string              - thread ID (empty if not a reply)
//	is_reply         bool                - true if the message is a reply
//	folder           string              - current folder ID
//	tags             list(string)        - current tags
//
// # Rule Evaluation
//
// All matching rules are applied in priority order (lower value = higher priority).
// Actions accumulate: if multiple rules set the folder, the last one wins.
// A rule with StopOnMatch=true stops further rule evaluation for that message.
//
// # Integration
//
// Wire the engine as a plugin and event subscriber:
//
//	engine, err := rules.NewEngine(provider, st)
//	svc, err := mailbox.New(mailbox.Config{},
//	    mailbox.WithStore(st),
//	    mailbox.WithPlugin(engine),   // registers AfterSend for send-side rules
//	)
//	svc.Connect(ctx)
//
//	// Subscribe receive-side rules via an adapter (rules can't import mailbox directly):
//	svc.Events().MessageReceived.Subscribe(ctx,
//	    func(ctx context.Context, ev event.Event[mailbox.MessageReceivedEvent], data mailbox.MessageReceivedEvent) error {
//	        return engine.OnMessageReceived(ctx, nil, rules.MessageReceivedData{
//	            MessageID:   data.MessageID,
//	            RecipientID: data.RecipientID,
//	        })
//	    },
//	    event.AsWorker("rules-engine"),
//	)
package rules
