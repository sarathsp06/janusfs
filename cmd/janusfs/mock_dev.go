package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/sarathsp06/janusfs/internal/api"
	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/obs"
	"github.com/sarathsp06/janusfs/internal/provider"
	"github.com/sarathsp06/janusfs/internal/ui"
)

// startMockMount starts a memory-only mock mount without actual FUSE kernel mounting.
// This is used for visual verification in containerized environments where FUSE is absent.
func startMockMount(parent context.Context, src, mountpoint, label string) (*mountRuntime, error) {
	eng, err := engine.New(src)
	if err != nil {
		// Create a temporary src directory to satisfy the engine
		tmpSrc := filepath.Join(os.TempDir(), "janus_mock_src")
		_ = os.MkdirAll(tmpSrc, 0o755)
		_ = os.WriteFile(filepath.Join(tmpSrc, ".janusignore"), []byte("*.secret\n"), 0o644)
		_ = os.WriteFile(filepath.Join(tmpSrc, ".janusmask"), []byte("*.env : env-value\n"), 0o644)
		_ = os.WriteFile(filepath.Join(tmpSrc, "app.env"), []byte("DB_PASSWORD=supersecure\n"), 0o644)
		_ = os.WriteFile(filepath.Join(tmpSrc, "db.secret"), []byte("unreachable secret\n"), 0o644)
		src = tmpSrc
		eng, err = engine.New(src)
		if err != nil {
			return nil, err
		}
	}

	prov := provider.NewRamCache(256<<20, 64<<20, 512<<20)

	tokenBytes := make([]byte, 16)
	_, _ = rand.Read(tokenBytes)
	bearerToken := hex.EncodeToString(tokenBytes)

	rt := &mountRuntime{
		UUID:       uuid.New().String(),
		Src:        src,
		Mountpoint: mountpoint,
		Token:      bearerToken,
		Label:      label,
		eng:        eng,
		prov:       prov,
		done:       make(chan struct{}),
	}

	recorder := obs.NewRecorder(nil)
	recorder.SetGeneration(eng.Generation())
	rt.recorder = recorder

	recorder.Emit(obs.Event{Op: obs.OpRead, Path: "README.md", Decision: obs.Allowed, Bytes: 3300, LatencyUs: 25, Cache: obs.CacheNA})
	recorder.Emit(obs.Event{Op: obs.OpRead, Path: "app.env", Decision: obs.Masked, Bytes: 1200, LatencyUs: 80, Cache: obs.CacheHit})
	recorder.Emit(obs.Event{Op: obs.OpOpen, Path: "db.secret", Decision: obs.Hidden, LatencyUs: 10, Cache: obs.CacheNA})

	apiSrv := api.New(ui.FS, bearerToken, recorder.Registry(), nil)
	apiSrv.SetMountInfo(src, mountpoint)
	apiSrv.SetVFSMeta(src, func() (int, int64, uint64, uint64, uint64) {
		return 1, 1024, 12, 3, 0
	}, func(relPath string, isDir bool) (string, []string, string) {
		res := eng.Resolve(relPath, isDir)
		return res.Decision.String(), res.PatternNames, res.RuleRef
	}, func() bool { return true }, eng.Generation)
	apiSrv.SetReload(rt.reload)
	rt.apiSrv = apiSrv

	return rt, nil
}
