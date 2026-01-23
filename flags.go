package mailbox

// Pre-allocated boolean pointers for efficient Flags creation.
// These avoid allocations when using MarkRead(), MarkUnread(), etc.
var (
	ptrTrue  = ptr(true)
	ptrFalse = ptr(false)
)

func ptr(b bool) *bool { return &b }

// Flags represents message flags that can be updated atomically.
// Use nil values to indicate no change.
type Flags struct {
	Read     *bool // nil = no change, true = mark read, false = mark unread
	Archived *bool // nil = no change, true = archive, false = unarchive
}

// Pre-allocated flag values for common operations.
// These are more efficient than calling MarkRead(), etc. in hot paths.
var (
	// FlagsMarkRead marks a message as read.
	FlagsMarkRead = Flags{Read: ptrTrue}
	// FlagsMarkUnread marks a message as unread.
	FlagsMarkUnread = Flags{Read: ptrFalse}
	// FlagsMarkArchived archives a message.
	FlagsMarkArchived = Flags{Archived: ptrTrue}
	// FlagsMarkUnarchived unarchives a message.
	FlagsMarkUnarchived = Flags{Archived: ptrFalse}
)

// NewFlags creates empty flags (no changes).
func NewFlags() Flags {
	return Flags{}
}

// WithRead returns flags with read status set.
func (f Flags) WithRead(read bool) Flags {
	if read {
		f.Read = ptrTrue
	} else {
		f.Read = ptrFalse
	}
	return f
}

// WithArchived returns flags with archived status set.
func (f Flags) WithArchived(archived bool) Flags {
	if archived {
		f.Archived = ptrTrue
	} else {
		f.Archived = ptrFalse
	}
	return f
}

// MarkRead returns flags to mark a message as read.
func MarkRead() Flags {
	return FlagsMarkRead
}

// MarkUnread returns flags to mark a message as unread.
func MarkUnread() Flags {
	return FlagsMarkUnread
}

// MarkArchived returns flags to archive a message.
func MarkArchived() Flags {
	return FlagsMarkArchived
}

// MarkUnarchived returns flags to unarchive a message.
func MarkUnarchived() Flags {
	return FlagsMarkUnarchived
}
