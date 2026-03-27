// Package presence provides user online/offline tracking with optional routing.
//
// Presence tracking is designed as an independent module that can be used
// by notification systems, chat, or any feature that needs to know whether
// a user is currently connected and where to reach them.
//
// # Tracker Interface
//
// The [Tracker] interface provides:
//   - Register/Unregister: mark users as online/offline
//   - IsOnline/OnlineUsers: check user status
//   - Locate: retrieve routing information for cross-instance delivery
//
// # Routing Information
//
// Registrations can carry optional routing info via [WithRouting].
// This enables cross-instance notification delivery without polling:
//
//	reg, _ := tracker.Register(ctx, userID, presence.WithRouting(presence.RoutingInfo{
//	    InstanceID: "web-server-3",
//	    Metadata:   map[string]string{"addr": "10.0.1.3:8080"},
//	}))
//	defer reg.Unregister(ctx)
//
//	// Later, from any instance:
//	info, _ := tracker.Locate(ctx, userID)
//	// info.InstanceID == "web-server-3"
//
// # Implementations
//
//   - [github.com/rbaliyan/mailbox/presence/memory]: In-memory tracker for single-instance and testing.
//   - [github.com/rbaliyan/mailbox/presence/redis]: Redis-backed tracker for multi-instance deployments.
//
// # Concurrency
//
// Multiple registrations for the same user are supported (e.g., multiple
// browser tabs or devices). The user remains online as long as at least
// one registration is active.
package presence
