// Package router held the Router and Registrar interfaces.
// These interfaces have moved to the mailbox package directly so that
// Router.Route can return mailbox.Mailbox without an import cycle.
//
// Replace any usage of router.Router with mailbox.Router and
// router.Registrar with mailbox.Registrar.
package router
