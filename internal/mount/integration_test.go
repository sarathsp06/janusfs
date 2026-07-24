//go:build fuseintegration

package mount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
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
	f, err = os.OpenFile(filepath.Join(mountpoint, ".janusignore"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		f.Close()
		t.Fatal("expected EACCES creating config file .janusignore, but succeeded")
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
		t.Fatal(".janusfs was not found in root directory listing")
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

func TestListxattrGating(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, ".janusignore"), "hidden.txt\n")
	writeFixture(t, filepath.Join(src, "hidden.txt"), "hidden")

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	// macOS / Linux direct syscall listxattr test.
	// In Go, on macOS or Linux, we can check listxattr using syscall.Listxattr.
	// But Listxattr might not be supported on all environments, or might return ENOTSUP.
	// However, if the file is HIDDEN, it must return EACCES instead of whatever it would normally return.
	// Let's do listxattr on the hidden file:
	_, err := syscall.Listxattr(filepath.Join(mountpoint, "hidden.txt"), nil)
	if err != syscall.EACCES {
		t.Errorf("expected EACCES on Listxattr of hidden file, got %v", err)
	}
}
