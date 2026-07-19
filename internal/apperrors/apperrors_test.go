package apperrors

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestToErrnoMapsEachSentinel(t *testing.T) {
	cases := []struct {
		err  error
		want syscall.Errno
	}{
		{ErrSymlinkEscape, syscall.ENOENT},
		{ErrRedactUnsupported, syscall.EACCES},
		{ErrRebuildTimeout, syscall.EIO},
		{ErrPanic, syscall.EIO},
		{errors.New("unrelated"), syscall.EIO},
	}
	for _, c := range cases {
		if got := ToErrno(c.err); got != c.want {
			t.Errorf("ToErrno(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestToErrnoMatchesWrappedSentinels(t *testing.T) {
	wrapped := fmt.Errorf("provider: rebuilding %q: %w", "/x/.env", ErrRebuildTimeout)
	if got := ToErrno(wrapped); got != syscall.EIO {
		t.Errorf("ToErrno(wrapped) = %v, want EIO", got)
	}
}
