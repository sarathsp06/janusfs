//go:build fuseintegration && (darwin || linux)

package mount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCreateGating(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, ".janusignore"), "id_rsa\n")
	writeFixture(t, filepath.Join(src, ".janusmask"), "secret.txt\n")

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	// 1. Creating a HIDDEN file (matches id_rsa) should fail with EACCES.
	f, err := os.OpenFile(filepath.Join(mountpoint, "id_rsa"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		f.Close()
		t.Fatal("expected EACCES creating hidden file id_rsa, but succeeded")
	}
	if !os.IsPermission(err) {
		t.Errorf("expected permission error, got %v", err)
	}

	// 2. Creating a MASKED file (matches secret.txt) should fail with EACCES.
	f, err = os.OpenFile(filepath.Join(mountpoint, "secret.txt"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		f.Close()
		t.Fatal("expected EACCES creating masked file secret.txt, but succeeded")
	}
	if !os.IsPermission(err) {
		t.Errorf("expected permission error, got %v", err)
	}

	// 3. Creating a config file should fail with EACCES.
	f, err = os.OpenFile(filepath.Join(mountpoint, ".janusfs.yml"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		f.Close()
		t.Fatal("expected EACCES creating config file .janusfs.yml, but succeeded")
	}
	if !os.IsPermission(err) {
		t.Errorf("expected permission error, got %v", err)
	}

	// 4. Creating an allowed file should succeed.
	f, err = os.OpenFile(filepath.Join(mountpoint, "allowed.txt"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("failed to create allowed file: %v", err)
	}
	_, err = f.Write([]byte("allowed content"))
	if err != nil {
		t.Fatalf("failed to write to allowed file: %v", err)
	}
	f.Close()
}

func TestVirtualDir(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, "test.txt"), "hello")

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	// 1. Verify ".janusfs" is in root readdir.
	entries, err := os.ReadDir(mountpoint)
	if err != nil {
		t.Fatalf("failed to read mountpoint root: %v", err)
	}

	foundVirtual := false
	for _, entry := range entries {
		if entry.Name() == ".janusfs" {
			foundVirtual = true
			if !entry.IsDir() {
				t.Error(".janusfs is not reported as a directory")
			}
		}
	}
	if !foundVirtual {
		if st, statErr := os.Stat(filepath.Join(mountpoint, ".janusfs")); statErr != nil || !st.IsDir() {
			t.Fatalf(".janusfs was not found in root directory listing and direct lookup failed: statErr=%v isDir=%v", statErr, statErr == nil && st.IsDir())
		}
		t.Log(".janusfs was not listed in root readdir, but direct lookup succeeded")
	}

	// 2. Read ".janusfs/status.json".
	statusPath := filepath.Join(mountpoint, ".janusfs", "status.json")
	statusBytes, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("failed to read .janusfs/status.json: %v", err)
	}

	var status map[string]interface{}
	if err := json.Unmarshal(statusBytes, &status); err != nil {
		t.Fatalf("invalid json in status.json: %v\nContent: %s", err, statusBytes)
	}

	if status["uptime"] == "" {
		t.Error("expected uptime to be present in status.json")
	}

	// 3. Read ".janusfs/conflicts.json".
	conflictsPath := filepath.Join(mountpoint, ".janusfs", "conflicts.json")
	conflictsBytes, err := os.ReadFile(conflictsPath)
	if err != nil {
		t.Fatalf("failed to read .janusfs/conflicts.json: %v", err)
	}

	var conflicts map[string]interface{}
	if err := json.Unmarshal(conflictsBytes, &conflicts); err != nil {
		t.Fatalf("invalid json in conflicts.json: %v\nContent: %s", err, conflictsBytes)
	}

	// 4. Try writing to virtual file -> should fail with EACCES.
	err = os.WriteFile(statusPath, []byte("hack"), 0o644)
	if err == nil {
		t.Fatal("expected EACCES writing to virtual file, but succeeded")
	}
}

func TestLinkDeniesLaunderingMaskedFile(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, ".janusmask"), "secret.env : env-value\n")
	writeFixture(t, filepath.Join(src, "secret.env"), "API_KEY=super-secret-value\n")

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	// Attempt to hardlink the masked file to an unmasked name. Without the fix,
	// this succeeds because Link only checked the new name's decision, never the
	// existing (masked) inode's own decision — one syscall away from reading the
	// plaintext through the new name.
	err := os.Link(filepath.Join(mountpoint, "secret.env"), filepath.Join(mountpoint, "copy.txt"))
	if err == nil {
		t.Fatal("expected EACCES hardlinking a masked file to a new name, but Link succeeded")
	}
	if !os.IsPermission(err) {
		t.Errorf("expected permission error, got %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(src, "copy.txt")); statErr == nil {
		t.Fatal("copy.txt should not have been created on disk")
	}
}

func TestListxattrGating(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, ".janusignore"), "hidden.txt\n")
	writeFixture(t, filepath.Join(src, "hidden.txt"), "hidden")

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	// macOS / Linux direct syscall listxattr test. golang.org/x/sys/unix is used
	// rather than the stdlib syscall package because syscall.Listxattr isn't
	// exposed on darwin's syscall package at all (only on linux's), while
	// unix.Listxattr is defined identically on both.
	// Listxattr might not be supported on all environments, or might return
	// ENOTSUP. However, if the file is HIDDEN, it must return EACCES instead of
	// whatever it would normally return. Let's do listxattr on the hidden file:
	_, err := unix.Listxattr(filepath.Join(mountpoint, "hidden.txt"), nil)
	if err != unix.EACCES && err != unix.ENOTSUP && err != unix.ENOSYS {
		t.Errorf("expected EACCES on Listxattr of hidden file, got %v", err)
	}
}

// TestReloadTakesEffectWithoutRemount asserts that a policy tightening (here,
// a file newly added to .janusfs.yml) is visible on the very next lookup and
// open of that path, with no remount. This is the behavioural counterpart to
// the zero attribute/entry/negative-lookup FUSE timeouts set in
// mount_darwin.go/mount_linux.go: if the kernel were allowed to cache a
// pre-reload lookup or attribute, a file just tightened to HIDDEN could keep
// answering from that cache instead of re-consulting the (already reloaded)
// engine.
func TestReloadTakesEffectWithoutRemount(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	target := filepath.Join(src, "soon-hidden.txt")
	writeFixture(t, target, "still readable for now")

	a, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	mountedPath := filepath.Join(mountpoint, "soon-hidden.txt")

	// Before: readable (no rules at all yet).
	if _, err := os.ReadFile(mountedPath); err != nil {
		t.Fatalf("expected the file to be readable before any rule exists, got %v", err)
	}

	// Tighten policy: add it to policy, then reload the SAME engine the
	// live mount already uses — no remount, no new Adapter.
	writeFixture(t, filepath.Join(src, ".janusignore"), "soon-hidden.txt\n")
	if err := a.Engine.Reload(src); err != nil {
		t.Fatalf("Engine.Reload: %v", err)
	}

	// After: a FRESH lookup+open of the same path, with no remount, must see
	// the new policy immediately.
	if _, err := os.ReadFile(mountedPath); err == nil {
		t.Fatal("expected EACCES reading a file just hidden by reload, but the read succeeded")
	} else if !os.IsPermission(err) {
		t.Errorf("expected a permission error after reload, got %v", err)
	}
}
