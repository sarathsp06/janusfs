package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sarathsp06/janusfs/internal/config"
)

func TestClassifyMountRecordsMergesLiveAndRecorded(t *testing.T) {
	live := []mountStatus{{Src: "/src/live", Mountpoint: "/mnt/live", Dashboard: "http://dash/live"}}
	records := []config.MountRecord{
		{Src: "/src/live", Mountpoint: "/mnt/live"},
		{Src: "/src/recorded", Mountpoint: "/mnt/recorded"},
	}
	statDir := func(path string) error { return nil }
	isMounted := func(path string) bool { return false }

	got := classifyMountRecords(live, records, statDir, isMounted)
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2: %#v", len(got), got)
	}
	if got[0].Status != "mounted" || got[0].Dashboard != "http://dash/live" {
		t.Fatalf("first listing should be mounted live entry, got %#v", got[0])
	}
	if got[1].Status != "recorded" {
		t.Fatalf("second listing should be recorded, got %#v", got[1])
	}
}

func TestClassifyMountRecordsMissingSrcAndStale(t *testing.T) {
	records := []config.MountRecord{
		{Src: "/src/missing", Mountpoint: "/mnt/missing"},
		{Src: "/src/stale", Mountpoint: "/mnt/stale"},
	}
	statDir := func(path string) error {
		if path == "/src/missing" {
			return errMountListingMissing
		}
		return nil
	}
	isMounted := func(path string) bool { return path == "/mnt/stale" }

	got := classifyMountRecords(nil, records, statDir, isMounted)
	if got[0].Status != "missing-src" {
		t.Fatalf("missing src status = %q", got[0].Status)
	}
	if got[1].Status != "stale" {
		t.Fatalf("stale status = %q", got[1].Status)
	}
}

func TestPrintMountListingsHuman(t *testing.T) {
	var b strings.Builder
	printMountListings(&b, []mountListing{{Status: "mounted", Src: "/src", Mountpoint: "/mnt"}})
	out := b.String()
	if !strings.Contains(out, "STATUS") || !strings.Contains(out, "mounted") || !strings.Contains(out, "/src") || !strings.Contains(out, "/mnt") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestMountListingsJSONShape(t *testing.T) {
	data, err := json.Marshal(mountListingsResponse{Mounts: []mountListing{{Status: "mounted", Src: "/src", Mountpoint: "/mnt"}}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Mounts []struct {
			Status     string `json:"status"`
			Src        string `json:"src"`
			Mountpoint string `json:"mountpoint"`
		} `json:"mounts"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].Status != "mounted" {
		t.Fatalf("unexpected JSON: %s", data)
	}
}
