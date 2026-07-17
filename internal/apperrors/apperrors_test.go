package apperrors

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestToErrno(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantErrno syscall.Errno
		wantEvent string
	}{
		{"hidden", ErrHidden, syscall.EACCES, "denied"},
		{"masked read-only", ErrMaskedReadOnly, syscall.EACCES, "denied"},
		{"rebuild timeout", ErrRebuildTimeout, syscall.EIO, "rebuild_timeout"},
		{"panic", ErrPanic, syscall.EIO, "panic"},
		{"config parse", ErrConfigParse, syscall.EACCES, "config_error"},
		{"symlink escape", ErrSymlinkEscape, syscall.ENOENT, "symlink_escape"},
		{"redact unsupported", ErrRedactUnsupported, syscall.EACCES, "redact_unsupported"},
		{"wrapped hidden", fmt.Errorf("wrap: %w", ErrHidden), syscall.EACCES, "denied"},
		{"wrapped rebuild timeout", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ErrRebuildTimeout)), syscall.EIO, "rebuild_timeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToErrno(tt.err); got != tt.wantErrno {
				t.Errorf("ToErrno(%v) = %v, want %v", tt.err, got, tt.wantErrno)
			}
			if got := Event(tt.err); got != tt.wantEvent {
				t.Errorf("Event(%v) = %q, want %q", tt.err, got, tt.wantEvent)
			}
		})
	}
}

func TestToErrnoFailClosedDefault(t *testing.T) {
	someOtherErr := errors.New("some unrelated failure")
	wrapped := fmt.Errorf("wrap: %w", someOtherErr)

	if got := ToErrno(someOtherErr); got != syscall.EIO {
		t.Errorf("ToErrno(unknown) = %v, want EIO (fail-closed default)", got)
	}
	if got := ToErrno(wrapped); got != syscall.EIO {
		t.Errorf("ToErrno(wrapped unknown) = %v, want EIO (fail-closed default)", got)
	}
	if got := ToErrno(nil); got != syscall.EIO {
		t.Errorf("ToErrno(nil) = %v, want EIO (fail-closed default, never leak as success)", got)
	}

	if got := Event(someOtherErr); got != "unknown" {
		t.Errorf("Event(unknown) = %q, want %q", got, "unknown")
	}
}

func TestSentinelsAreDistinguishable(t *testing.T) {
	sentinels := []error{
		ErrHidden, ErrMaskedReadOnly, ErrRebuildTimeout, ErrPanic,
		ErrConfigParse, ErrSymlinkEscape, ErrRedactUnsupported,
		ErrNotFound, ErrAlreadyExists,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("sentinel %v unexpectedly matches %v via errors.Is", a, b)
			}
		}
	}
}

func TestNotFoundAndAlreadyExistsFailClosed(t *testing.T) {
	// These sentinels don't appear in the SPEC §13 matrix; ToErrno must
	// still fail closed to EIO rather than leaking them unmapped.
	if got := ToErrno(ErrNotFound); got != syscall.EIO {
		t.Errorf("ToErrno(ErrNotFound) = %v, want EIO", got)
	}
	if got := ToErrno(ErrAlreadyExists); got != syscall.EIO {
		t.Errorf("ToErrno(ErrAlreadyExists) = %v, want EIO", got)
	}
}
