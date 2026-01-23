package store

import "errors"

// Sentinel errors for the store package.
var (
	// ErrNotFound is returned when a message cannot be found.
	ErrNotFound = errors.New("store: not found")

	// ErrInvalidID is returned when an invalid ID is provided.
	ErrInvalidID = errors.New("store: invalid id")

	// ErrDuplicateEntry is returned when a duplicate entry is detected.
	ErrDuplicateEntry = errors.New("store: duplicate entry")

	// ErrNotConnected is returned when operations are attempted before Connect().
	ErrNotConnected = errors.New("store: not connected")

	// ErrAlreadyConnected is returned when Connect() is called twice.
	ErrAlreadyConnected = errors.New("store: already connected")

	// ErrEmptyRecipients is returned when no recipients are provided.
	ErrEmptyRecipients = errors.New("store: empty recipients")

	// ErrEmptySubject is returned when subject is empty.
	ErrEmptySubject = errors.New("store: empty subject")

	// ErrFilterInvalid is returned when a filter is invalid.
	ErrFilterInvalid = errors.New("store: invalid filter")

	// ErrRegexSearchDisabled is returned when text search is attempted but regex is disabled.
	ErrRegexSearchDisabled = errors.New("store: regex search is disabled")

	// ErrInvalidFolderID is returned when an invalid folder ID is provided.
	ErrInvalidFolderID = errors.New("store: invalid folder id")

	// ErrInvalidIdempotencyKey is returned when an empty idempotency key is provided.
	ErrInvalidIdempotencyKey = errors.New("store: invalid idempotency key")

	// ErrTransactionFailed is returned when a database transaction fails.
	// This indicates the atomic operation could not complete and no changes were made.
	ErrTransactionFailed = errors.New("store: transaction failed")
)

// Error checking helpers.

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func IsInvalidID(err error) bool {
	return errors.Is(err, ErrInvalidID)
}

func IsDuplicateEntry(err error) bool {
	return errors.Is(err, ErrDuplicateEntry)
}

func IsNotConnected(err error) bool {
	return errors.Is(err, ErrNotConnected)
}
