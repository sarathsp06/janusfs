package rules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// caseInsensitiveVolume reports whether dir lives on a volume that treats two
// spellings of a name as the same file — the default for APFS and HFS+, and
// the reason glob matching must fold case there: the kernel already agreed
// that ".ENV" and ".env" name the same inode, so the policy engine's answer
// must agree too, or a case-flipped spelling walks straight past a mask rule.
//
// The probe needs no write access: it case-flips dir's own basename and checks
// whether that spelling resolves to the same inode via os.SameFile, which is
// portable across platforms and doesn't require syscall.Stat_t plumbing.
func caseInsensitiveVolume(dir string) bool {
	base := filepath.Base(dir)
	flipped := flipCase(base)
	if flipped == base {
		// No cased letters in the basename (e.g. root "/", or a name made
		// entirely of digits/symbols) — nothing to probe. Fall back to the
		// platform default: darwin ships case-insensitive by default, Linux
		// filesystems are case-sensitive by default.
		return runtime.GOOS == "darwin"
	}

	parent := filepath.Dir(dir)
	origInfo, err := os.Stat(dir)
	if err != nil {
		return runtime.GOOS == "darwin"
	}
	flippedInfo, err := os.Stat(filepath.Join(parent, flipped))
	if err != nil {
		// The flipped spelling doesn't resolve at all: case-sensitive.
		return false
	}
	return os.SameFile(origInfo, flippedInfo)
}

// flipCase inverts the case of every letter in s: upper becomes lower and
// lower becomes upper. Used only to build a variant spelling for the
// case-insensitivity probe above, never for matching itself.
func flipCase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
