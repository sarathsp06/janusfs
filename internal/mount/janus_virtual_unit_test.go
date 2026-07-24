package mount

import (
	"context"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/provider"
)

func TestVirtualDirUnit(t *testing.T) {
	eng, err := engine.New(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	prov := provider.NewRamCache(1000, 1000, 1000)
	jr := &JanusRoot{
		Engine:    eng,
		Provider:  prov,
		StartTime: time.Now().Add(-5 * time.Hour),
	}

	dir := &janusVirtualDir{root: jr}

	// 1. Getattr of directory
	var outAttr fuse.AttrOut
	errno := dir.Getattr(context.Background(), nil, &outAttr)
	if errno != 0 {
		t.Fatalf("unexpected errno on dir Getattr: %v", errno)
	}
	if outAttr.Mode&0170000 != 0040000 { // S_IFDIR
		t.Errorf("expected S_IFDIR mode, got %o", outAttr.Mode)
	}

	// 2. Readdir of directory
	stream, readdirErrno := dir.Readdir(context.Background())
	if readdirErrno != 0 {
		t.Fatalf("unexpected errno on dir Readdir: %v", readdirErrno)
	}
	defer stream.Close()

	var entries []string
	for stream.HasNext() {
		entry, errno := stream.Next()
		if errno != 0 {
			t.Fatalf("unexpected errno during dir stream iteration: %v", errno)
		}
		entries = append(entries, entry.Name)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(entries), entries)
	}
	if entries[0] != "conflicts.json" || entries[1] != "status.json" {
		t.Errorf("expected conflicts.json and status.json, got %v", entries)
	}

	// 3. File content generation
	fileConflicts := &janusVirtualFile{root: jr, name: "conflicts.json"}
	fileStatus := &janusVirtualFile{root: jr, name: "status.json"}

	conflictsContent := fileConflicts.content()
	if len(conflictsContent) == 0 {
		t.Error("expected conflicts.json content to be non-empty")
	}

	statusContent := fileStatus.content()
	if len(statusContent) == 0 {
		t.Error("expected status.json content to be non-empty")
	}
}
