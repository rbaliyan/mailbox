// Package notify provides per-user real-time notification streams.
//
// Notifications are delivered in real-time to connected users via [Stream] and
// persisted in a [Store] for backfill on reconnect. The [Notifier] coordinates
// between presence tracking, notification storage, and optional cross-instance
// routing.
//
// # Architecture
//
// The notification system uses the event bus with AsWorker (worker model),
// meaning each mailbox event is processed by exactly one instance:
//
//	Mailbox Event (MessageReceived, etc.)
//	  → Event Bus (AsWorker — one worker per event)
//	    → Notifier.Push() checks presence
//	      → Store.Save() (for backfill on reconnect)
//	      → Local stream delivery (if user connected to this instance)
//	      → Router.Route() (if user connected to another instance)
//
// # Basic Usage
//
// Configure the service with a notifier:
//
//	notifier := notify.NewNotifier(
//	    notify.WithStore(notifyStore),
//	    notify.WithPresence(tracker),
//	)
//
//	svc, _ := mailbox.New(mailbox.Config{},
//	    mailbox.WithStore(store),
//	    mailbox.WithNotifier(notifier),
//	)
//
// Open a notification stream (e.g., in an SSE handler):
//
//	// Register presence
//	reg, _ := tracker.Register(ctx, userID, presence.WithRouting(...))
//	defer reg.Unregister(ctx)
//
//	// Open stream with backfill from last seen event
//	stream, _ := svc.Notifications(ctx, userID, lastEventID)
//	defer stream.Close()
//
//	for {
//	    evt, err := stream.Next(r.Context())
//	    if err != nil {
//	        return
//	    }
//	    fmt.Fprintf(w, "id: %s\ndata: %s\n\n", evt.ID, evt.Payload)
//	    flusher.Flush()
//	}
//
// # Cross-Instance Delivery
//
// Without a [Router], remote instances discover events via store polling
// (configurable interval, default 2s). With a Router and presence routing
// info, events are forwarded directly to the instance holding the connection:
//
//	notifier := notify.NewNotifier(
//	    notify.WithStore(store),
//	    notify.WithPresence(tracker),
//	    notify.WithRouter(myRouter),
//	    notify.WithInstanceID("web-server-3"),
//	)
//
// # Implementations
//
//   - [github.com/rbaliyan/mailbox/notify/memory]: In-memory notification store for testing.
package notify
