package mailbox

import "testing"

// boolEq compares an optional bool pointer against an expectation.
// want == nil asserts p is nil; otherwise p must be non-nil and equal *want.
func boolEq(t *testing.T, name string, p *bool, want *bool) {
	t.Helper()
	switch {
	case want == nil && p != nil:
		t.Errorf("%s = %v, want nil", name, *p)
	case want == nil:
		// both nil, ok
	case p == nil:
		t.Errorf("%s = nil, want %v", name, *want)
	case *p != *want:
		t.Errorf("%s = %v, want %v", name, *p, *want)
	}
}

func TestNewFlags(t *testing.T) {
	f := NewFlags()
	if f.Read != nil {
		t.Errorf("NewFlags().Read = %v, want nil", *f.Read)
	}
	if f.Archived != nil {
		t.Errorf("NewFlags().Archived = %v, want nil", *f.Archived)
	}
}

func TestFlagsBuilders(t *testing.T) {
	tr, fa := true, false

	tests := []struct {
		name         string
		flags        Flags
		wantRead     *bool
		wantArchived *bool
	}{
		{"WithRead true", NewFlags().WithRead(true), &tr, nil},
		{"WithRead false", NewFlags().WithRead(false), &fa, nil},
		{"WithArchived true", NewFlags().WithArchived(true), nil, &tr},
		{"WithArchived false", NewFlags().WithArchived(false), nil, &fa},
		{"WithRead then WithArchived", NewFlags().WithRead(true).WithArchived(false), &tr, &fa},
		{"WithArchived then WithRead", NewFlags().WithArchived(true).WithRead(false), &fa, &tr},
		{"MarkRead", MarkRead(), &tr, nil},
		{"MarkUnread", MarkUnread(), &fa, nil},
		{"MarkArchived", MarkArchived(), nil, &tr},
		{"MarkUnarchived", MarkUnarchived(), nil, &fa},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			boolEq(t, "Read", tc.flags.Read, tc.wantRead)
			boolEq(t, "Archived", tc.flags.Archived, tc.wantArchived)
		})
	}
}

// TestFlagsWithReadOverwrite verifies that WithRead overwrites a prior value
// rather than leaving it set, since the builder returns a value receiver copy.
func TestFlagsWithReadOverwrite(t *testing.T) {
	f := NewFlags().WithRead(true).WithRead(false)
	if f.Read == nil || *f.Read {
		t.Errorf("Read = %v, want false after overwrite", f.Read)
	}
}

// TestMarkHelpersShareValue verifies the Mark* helpers return the package-level
// pre-allocated Flags values (no per-call allocation of the pointers).
func TestMarkHelpersShareValue(t *testing.T) {
	if MarkRead().Read != FlagsMarkRead.Read {
		t.Error("MarkRead does not reuse FlagsMarkRead.Read pointer")
	}
	if MarkUnread().Read != FlagsMarkUnread.Read {
		t.Error("MarkUnread does not reuse FlagsMarkUnread.Read pointer")
	}
	if MarkArchived().Archived != FlagsMarkArchived.Archived {
		t.Error("MarkArchived does not reuse FlagsMarkArchived.Archived pointer")
	}
	if MarkUnarchived().Archived != FlagsMarkUnarchived.Archived {
		t.Error("MarkUnarchived does not reuse FlagsMarkUnarchived.Archived pointer")
	}
}
