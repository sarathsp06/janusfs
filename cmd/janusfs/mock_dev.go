package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

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

	eventBus := obs.NewEventBus(4096)
	metrics := &obs.JanusMetrics{}
	ringBuf := obs.NewRingBuffer(8192)
	topN := obs.NewTopN(1000)

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
		metrics:    metrics,
		eventBus:   eventBus,
		done:       make(chan struct{}),
	}

	metrics.Generation.Store(eng.Generation())

	// Populate some mock ops/bytes/events/topN to look awesome on the dashboard
	metrics.RecordOp(obs.OpRead, obs.Allowed)
	metrics.RecordOp(obs.OpRead, obs.Allowed)
	metrics.RecordOp(obs.OpRead, obs.Masked)
	metrics.RecordOp(obs.OpOpen, obs.Hidden)
	metrics.RecordBytes(obs.Allowed, 4500)
	metrics.RecordBytes(obs.Masked, 1200)

	ringBuf.Push(`{"TS":"` + time.Now().Format(time.RFC3339) + `","Op":"read","Path":"app.env","Decision":1,"Bytes":24,"LatencyUs":140,"Cache":1}`)
	ringBuf.Push(`{"TS":"` + time.Now().Format(time.RFC3339) + `","Op":"open","Path":"db.secret","Decision":2,"Bytes":0,"LatencyUs":42,"Cache":0}`)

	topN.Record("app.env", 1200)
	topN.Record("README.md", 3300)

	apiSrv := api.New(ui.FS, bearerToken, metrics, ringBuf, topN, eventBus, nil)
	apiSrv.SetMountInfo(src, mountpoint)
	apiSrv.SetVFSMeta(src, func() (int, int64, uint64, uint64, uint64) {
		return 1, 1024, 12, 3, 0
	}, func(relPath string, isDir bool) (string, []string, string) {
		res := eng.Resolve(relPath, isDir)
		return res.Decision.String(), res.PatternNames, res.RuleRef
	}, func() bool { return true })
	apiSrv.SetReload(rt.reload)
	rt.apiSrv = apiSrv

	return rt, nil
}
