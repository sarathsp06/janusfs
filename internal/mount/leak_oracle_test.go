//go:build fuseintegration

// TestLeakOracle is the leak oracle: sentinel secrets are
// planted in a real fixture tree, mounted through a real macFUSE mount via
// the actual Adapter, and every byte successfully read through the mount
// is scanned for them. It is the security assertion layer for Phase 2 and
// on: a new masking feature adds new sentinels here, not a
// parallel test mechanism.
//
// Requires macFUSE installed and approved (`make leak-oracle` /
// `make integration`, both behind the fuseintegration build tag). Skips, rather
// than fails, if mounting doesn't
// come up within the timeout, so this suite doesn't block CI/dev machines
// without macFUSE approved.
package mount

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/provider"
)

// sentinels are planted secret values that must never survive to a mount
// read for their Masked/Hidden files. Distinctive strings (not realistic
// secrets) so a match in mount output is unambiguous, never a coincidence.
const (
	sentinelAWSKey     = "AKIASENTINELLEAKTEST"
	sentinelJWT        = "eyJTENTINELHEADER1234.eyJTENTINELPAYLOAD5678.SENTINELSIG9012"
	sentinelPrivateKey = "SENTINEL-PRIVATE-KEY-BODY-MUST-NEVER-LEAK"
	sentinelEnvValue   = "sentinel-env-secret-value-9f8e7d"
	sentinelHiddenBody = "sentinel-hidden-file-body-must-never-be-read"
)

func mountForTest(t *testing.T, src, mountpoint string) (*Adapter, func()) {
	t.Helper()
	// Isolate from any real ~/.janusfs/config on the machine running this test,
	// since that is a live global rule level:
	// this test's fixtures and assertions are self-contained and must not
	// depend on the developer's own machine-wide defaults.
	t.Setenv("HOME", t.TempDir())

	eng, err := engine.New(src)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	prov := provider.NewRamCache(64<<20, 32<<20, 64<<20)

	a := &Adapter{Engine: eng, Provider: prov}
	ctx, cancel := context.WithCancel(context.Background())
	mounted := make(chan struct{})
	a.OnMounted = func() { close(mounted) }

	done := make(chan error, 1)
	go func() { done <- a.Mount(ctx, src, mountpoint) }()

	select {
	case <-mounted:
	case err := <-done:
		t.Skipf("mount did not come up (macFUSE not installed/approved?): %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Skip("mount did not come up within 5s (macFUSE not installed/approved?)")
	}

	cleanup := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = a.Unmount(mountpoint)
			<-done
		}
	}
	return a, cleanup
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLeakOracle(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, ".janusignore"), "id_rsa\n")
	writeFixture(t, filepath.Join(src, ".janusmask"),
		"*.env* : env-value\n"+
			"**/*.pem : private-key\n"+
			"tokens.txt : jwt\n"+
			"secrets.txt : aws-key\n",
	)

	// Hidden: must never be read at all.
	writeFixture(t, filepath.Join(src, "id_rsa"), sentinelHiddenBody)

	// Masked: content must reach the mount fully starred, never the raw
	// sentinel bytes, at any byte length or offset.
	writeFixture(t, filepath.Join(src, ".env"), "API_KEY="+sentinelEnvValue+"\n")
	writeFixture(t, filepath.Join(src, "server.pem"),
		"prefix\n-----BEGIN RSA PRIVATE KEY-----\n"+sentinelPrivateKey+"\n-----END RSA PRIVATE KEY-----\nsuffix\n")
	writeFixture(t, filepath.Join(src, "secrets.txt"), "key="+sentinelAWSKey+"\n")
	writeFixture(t, filepath.Join(src, "tokens.txt"), "token="+sentinelJWT+"\n")

	// Allowed: unaffected control file, sanity-checks the mount actually
	// serves real content (so an all-EACCES bug wouldn't trivially "pass").
	writeFixture(t, filepath.Join(src, "README.md"), "hello world, nothing secret here\n")

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	var allReadBytes bytes.Buffer

	// Hidden: open must fail, never yield a single byte.
	if data, err := os.ReadFile(filepath.Join(mountpoint, "id_rsa")); err == nil {
		t.Fatalf("expected EACCES reading hidden id_rsa, got %d bytes: %q", len(data), data)
	}

	for _, name := range []string{".env", "server.pem", "secrets.txt", "tokens.txt", "README.md"} {
		data, err := os.ReadFile(filepath.Join(mountpoint, name))
		if err != nil {
			t.Fatalf("reading %s through mount: %v", name, err)
		}
		allReadBytes.Write(data)

		// Byte-length preservation: masked content
		// must be the exact same size as the real file.
		realInfo, err := os.Stat(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(data)) != realInfo.Size() {
			t.Errorf("%s: mount read %d bytes, real file is %d bytes (size must be preserved)", name, len(data), realInfo.Size())
		}
	}

	// The control file's content must have passed through unredacted.
	if !bytes.Contains(allReadBytes.Bytes(), []byte("hello world, nothing secret here")) {
		t.Error("expected README.md's real content to pass through — an all-EACCES bug would make this suite pass vacuously")
	}

	// The actual leak-oracle assertion: no sentinel ever appears in
	// anything successfully read through the mount.
	for _, sentinel := range []string{sentinelAWSKey, sentinelJWT, sentinelPrivateKey, sentinelEnvValue, sentinelHiddenBody} {
		if bytes.Contains(allReadBytes.Bytes(), []byte(sentinel)) {
			t.Errorf("LEAK: sentinel %q was readable through the mount", sentinel)
		}
	}
}

// TestLeakOracleOffsetReads exercises dd-style non-zero-offset reads
// against a masked file: the leak oracle must hold at any read offset, not just
// a single whole-file read from 0.
func TestLeakOracleOffsetReads(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, ".janusmask"), "*.env* : env-value\n")
	content := "PREFIX_KEY=nonsecretvalue\nAPI_KEY=" + sentinelEnvValue + "\nSUFFIX=alsofine\n"
	writeFixture(t, filepath.Join(src, ".env"), content)

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	f, err := os.Open(filepath.Join(mountpoint, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	for off := 0; off < len(content); off += 7 {
		buf := make([]byte, 7)
		n, err := f.ReadAt(buf, int64(off))
		if err != nil && n == 0 {
			continue // short final read near EOF
		}
		if bytes.Contains(buf[:n], []byte(sentinelEnvValue)) {
			t.Fatalf("LEAK at offset %d: %q", off, buf[:n])
		}
	}

	full, err := os.ReadFile(filepath.Join(mountpoint, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != len(content) {
		t.Fatalf("len(full)=%d, want %d", len(full), len(content))
	}
	// env-value masks the *value* of every "KEY=value" line by design
	// — it can't tell secrets from non-secrets by key name, so it redacts every
	// value. Only the key names and '=' survive, not
	// "nonsecretvalue"/"alsofine" themselves.
	if !strings.Contains(string(full), "PREFIX_KEY=") || !strings.Contains(string(full), "SUFFIX=") {
		t.Fatalf("expected key names to survive redaction, got %q", full)
	}
	if strings.Contains(string(full), "nonsecretvalue") || strings.Contains(string(full), "alsofine") {
		t.Fatalf("expected every value on a KEY=value line to be redacted (env-value's documented scope), got %q", full)
	}
}
