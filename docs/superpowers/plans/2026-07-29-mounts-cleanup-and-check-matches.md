# Mounts Cleanup and Check Matches Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add self-healing mount registry cleanup, a `janusfs mounts` command, and `janusfs check --matches` policy preview.

**Architecture:** Keep mount state classification in `cmd/janusfs` near daemon/control code, because it depends on daemon live state, `mounts.json`, pidfiles, and platform mount checks. Extend `internal/check` to optionally return policy matches from the same tree walk it already performs, then let `cmd/janusfs/check.go` render those matches in human and JSON output.

**Tech Stack:** Go, Cobra CLI, existing `internal/config` mount registry, existing daemon control protocol, existing `internal/check` and `internal/engine` policy resolution.

## Global Constraints

- Keep `janusfs check <dir>` lint-only by default.
- `janusfs check <dir> --matches` lists only `HIDDEN` and `MASKED` paths by default.
- Add `janusfs mounts` and `janusfs mounts --json`.
- Registry cleanup is best-effort and must not prevent daemon startup.
- Keep `janusfs paths`; clarify it as config/data paths only.
- Do not stage or commit unrelated root policy-file working-tree changes (`.janusignore`, `.janusmask`, `.janusfs.yml`).

---

## File structure

- `internal/check/check.go`: add `Options.Matches`, `Report.Matches`, and `Match` structs. Compute matches while reusing existing `walkTree` and `engine.Resolve`.
- `cmd/janusfs/check.go`: add `--matches`, include matches in JSON output, and append human-readable match sections.
- `cmd/janusfs/mounts.go`: new command that classifies active and recorded mounts, renders table and JSON.
- `cmd/janusfs/main.go`: register `newMountsCmd()`.
- `cmd/janusfs/daemon.go`: reuse the mount classification/cleanup helper during resume/startup; prune missing-source and unrecoverable stale records.
- `cmd/janusfs/daemon_test.go` or `cmd/janusfs/mounts_test.go`: tests for classification, command output, and cleanup.
- `README.md`, `SPEC.md`, `docs/knowledge/cli-and-daemon.md`, `docs/knowledge/log.md`: update after implementation.

---

### Task 1: Add `janusfs check --matches`

**Files:**
- Modify: `internal/check/check.go`
- Modify: `cmd/janusfs/check.go`
- Test: `internal/check/check_test.go`
- Test: `cmd/janusfs/check_test.go`

**Interfaces:**
- Consumes: `engine.Engine.Resolve(rel string, isDir bool) engine.Resolution`
- Produces:
  - `check.Options{Matches bool}`
  - `check.Match{Decision string, Path string, RuleRef string, PatternNames []string}`
  - `check.Report.Matches []Match`
  - CLI flag `janusfs check [path] --matches`

- [ ] **Step 1: Write failing internal/check test for matches**

Add to `internal/check/check_test.go`:

```go
func TestCheckMatchesReportsHiddenAndMaskedOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusfs.yml"), `version: 1
hide:
  - "*.pem"
mask:
  - paths:
      - "*.env"
    patterns:
      - env-value
`)
	writeFile(t, filepath.Join(root, "server.pem"), "x")
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=secret\n")
	writeFile(t, filepath.Join(root, "README.md"), "hello")

	r, err := RunWithOptions(root, Options{Matches: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Matches) != 2 {
		t.Fatalf("expected hidden+masked matches only, got %#v", r.Matches)
	}
	got := map[string]string{}
	for _, m := range r.Matches {
		got[m.Path] = m.Decision
	}
	if got["server.pem"] != "HIDDEN" {
		t.Fatalf("server.pem decision = %q, want HIDDEN; matches=%#v", got["server.pem"], r.Matches)
	}
	if got[".env"] != "MASKED" {
		t.Fatalf(".env decision = %q, want MASKED; matches=%#v", got[".env"], r.Matches)
	}
	if _, ok := got["README.md"]; ok {
		t.Fatalf("ALLOWED README.md should not be included by --matches: %#v", r.Matches)
	}
}
```

- [ ] **Step 2: Run failing test**

Run:

```bash
go test ./internal/check -run TestCheckMatchesReportsHiddenAndMaskedOnly -count=1
```

Expected: compile failure because `Options.Matches`, `Report.Matches`, and `Match` do not exist.

- [ ] **Step 3: Implement check match collection**

In `internal/check/check.go`, add:

```go
type Match struct {
	Decision     string   `json:"decision"`
	Path         string   `json:"path"`
	RuleRef      string   `json:"ruleRef,omitempty"`
	PatternNames []string `json:"patternNames,omitempty"`
}
```

Extend `Report`:

```go
type Report struct {
	Findings  []Finding `json:"findings"`
	Matches   []Match   `json:"matches,omitempty"`
	FileCount int       `json:"fileCount"`
	DirCount  int       `json:"dirCount"`
}
```

Extend `Options`:

```go
type Options struct {
	Secrets bool
	Matches bool
}
```

In `RunWithOptions`, after `entries` are collected and before returning, compute:

```go
var matches []Match
if opts.Matches {
	matches = policyMatches(eng, entries)
}
return Report{Findings: findings, Matches: matches, FileCount: fileCount, DirCount: dirCount}, nil
```

Add helper:

```go
func policyMatches(eng *engine.Engine, entries []treeEntry) []Match {
	var out []Match
	for _, te := range entries {
		res := eng.Resolve(te.rel, te.isDir)
		switch res.Decision {
		case engine.Hidden, engine.Masked:
			out = append(out, Match{
				Decision:     res.Decision.String(),
				Path:         te.rel,
				RuleRef:      res.RuleRef,
				PatternNames: append([]string(nil), res.PatternNames...),
			})
		}
	}
	return out
}
```

- [ ] **Step 4: Verify internal/check test passes**

Run:

```bash
go test ./internal/check -run TestCheckMatchesReportsHiddenAndMaskedOnly -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing CLI tests for human and JSON output**

Add to `cmd/janusfs/check_test.go`:

```go
func TestRunCheckMatchesHumanOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusfs.yml"), `version: 1
hide:
  - "*.pem"
mask:
  - paths:
      - "*.env"
    patterns:
      - env-value
`)
	writeFile(t, filepath.Join(root, "server.pem"), "x")
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=secret\n")

	out := captureStdout(t, func() {
		if err := runCheck(root, false, false, true); err != nil {
			t.Fatalf("runCheck returned error: %v", err)
		}
	})
	if !strings.Contains(out, "Policy matches:") {
		t.Fatalf("expected Policy matches section, got %q", out)
	}
	if !strings.Contains(out, "HIDDEN") || !strings.Contains(out, "server.pem") {
		t.Fatalf("expected hidden match, got %q", out)
	}
	if !strings.Contains(out, "MASKED") || !strings.Contains(out, ".env") || !strings.Contains(out, "env-value") {
		t.Fatalf("expected masked match with pattern name, got %q", out)
	}
}

func TestRunCheckMatchesJSONOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusfs.yml"), "version: 1\nhide:\n  - \"*.pem\"\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")

	out := captureStdout(t, func() {
		if err := runCheck(root, true, false, true); err != nil {
			t.Fatalf("runCheck returned error: %v", err)
		}
	})
	var report struct {
		Matches []struct {
			Decision string `json:"decision"`
			Path     string `json:"path"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(report.Matches) != 1 || report.Matches[0].Decision != "HIDDEN" || report.Matches[0].Path != "server.pem" {
		t.Fatalf("unexpected matches: %#v", report.Matches)
	}
}
```

- [ ] **Step 6: Run failing CLI tests**

Run:

```bash
go test ./cmd/janusfs -run 'TestRunCheckMatches(Human|JSON)Output' -count=1
```

Expected: compile failure because `runCheck` still takes three arguments.

- [ ] **Step 7: Implement CLI flag and output**

In `cmd/janusfs/check.go`:

- Add `var matches bool` in `newCheckCmd`.
- Pass it to `runCheck(dir, jsonOut, secrets, matches)`.
- Add flag:

```go
cmd.Flags().BoolVar(&matches, "matches", false, "also list files and directories that currently resolve Hidden or Masked")
```

Change signature:

```go
func runCheck(dir string, jsonOut bool, secrets bool, matches bool) error
```

Call:

```go
report, err := check.RunWithOptions(dir, check.Options{Secrets: secrets, Matches: matches})
```

After `printCheckReport(report)`, append:

```go
if matches {
	printCheckMatches(report.Matches)
}
```

Add:

```go
func printCheckMatches(matches []check.Match) {
	fmt.Println()
	fmt.Println("Policy matches:")
	if len(matches) == 0 {
		fmt.Println("  none")
		return
	}
	for _, decision := range []string{"HIDDEN", "MASKED"} {
		printedHeader := false
		for _, m := range matches {
			if m.Decision != decision {
				continue
			}
			if !printedHeader {
				fmt.Println()
				fmt.Println(decision)
				printedHeader = true
			}
			extra := m.RuleRef
			if len(m.PatternNames) > 0 {
				extra = strings.TrimSpace(extra + "  [" + strings.Join(m.PatternNames, " ") + "]")
			}
			if extra == "" {
				fmt.Printf("  %s\n", m.Path)
			} else {
				fmt.Printf("  %-32s %s\n", m.Path, extra)
			}
		}
	}
}
```

Add `strings` import to `cmd/janusfs/check.go`.

Update existing tests that call `runCheck(root, jsonOut, secrets)` to pass `false` as the fourth argument.

- [ ] **Step 8: Verify CLI tests pass**

Run:

```bash
go test ./cmd/janusfs -run 'TestRunCheck' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 1**

Run:

```bash
git add internal/check/check.go internal/check/check_test.go cmd/janusfs/check.go cmd/janusfs/check_test.go
git commit -m "feat: add check matches preview"
```

---

### Task 2: Add mount record classification and `janusfs mounts`

**Files:**
- Create: `cmd/janusfs/mounts.go`
- Modify: `cmd/janusfs/main.go`
- Test: `cmd/janusfs/mounts_test.go`

**Interfaces:**
- Consumes: `config.LoadMounts()`, `callDaemon("mounts", daemonRequest{Cmd:"list"})`, `mountpointMounted(path string) bool`
- Produces:
  - `newMountsCmd() *cobra.Command`
  - `mountRecordStatus` constants: `mounted`, `recorded`, `missing-src`, `stale`, `error`
  - `classifyMountRecords(live []mountStatus, records []config.MountRecord) []mountListing`

- [ ] **Step 1: Write failing classification test**

Create `cmd/janusfs/mounts_test.go`:

```go
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
```

- [ ] **Step 2: Run failing classification test**

Run:

```bash
go test ./cmd/janusfs -run TestClassifyMountRecords -count=1
```

Expected: compile failure because `classifyMountRecords` and `errMountListingMissing` do not exist.

- [ ] **Step 3: Implement `cmd/janusfs/mounts.go` classification**

Create `cmd/janusfs/mounts.go` with:

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
)

var errMountListingMissing = errors.New("missing source")

type mountListing struct {
	Status     string `json:"status"`
	Src        string `json:"src"`
	Mountpoint string `json:"mountpoint"`
	Label      string `json:"label,omitempty"`
	Dashboard  string `json:"dashboard,omitempty"`
	Error      string `json:"error,omitempty"`
}

type mountListingsResponse struct {
	Mounts []mountListing `json:"mounts"`
}

type statDirFunc func(string) error
type isMountedFunc func(string) bool

func defaultStatDir(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return errMountListingMissing
	}
	if !st.IsDir() {
		return errMountListingMissing
	}
	return nil
}

func classifyMountRecords(live []mountStatus, records []config.MountRecord, statDir statDirFunc, isMounted isMountedFunc) []mountListing {
	byMountpoint := map[string]mountListing{}
	for _, m := range live {
		byMountpoint[m.Mountpoint] = mountListing{Status: "mounted", Src: m.Src, Mountpoint: m.Mountpoint, Label: m.Label, Dashboard: m.Dashboard}
	}
	for _, rec := range records {
		if _, ok := byMountpoint[rec.Mountpoint]; ok {
			continue
		}
		listing := mountListing{Src: rec.Src, Mountpoint: rec.Mountpoint, Label: rec.Label}
		if err := statDir(rec.Src); err != nil {
			listing.Status = "missing-src"
			listing.Error = err.Error()
		} else if isMounted(rec.Mountpoint) {
			listing.Status = "stale"
		} else {
			listing.Status = "recorded"
		}
		byMountpoint[rec.Mountpoint] = listing
	}
	out := make([]mountListing, 0, len(byMountpoint))
	for _, listing := range byMountpoint {
		out = append(out, listing)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status < out[j].Status
		}
		return out[i].Mountpoint < out[j].Mountpoint
	})
	return out
}
```

- [ ] **Step 4: Verify classification tests pass**

Run:

```bash
go test ./cmd/janusfs -run TestClassifyMountRecords -count=1
```

Expected: PASS.

- [ ] **Step 5: Add failing command output tests**

Append to `cmd/janusfs/mounts_test.go`:

```go
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
			Status string `json:"status"`
			Src string `json:"src"`
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
```

- [ ] **Step 6: Run failing command output tests**

Run:

```bash
go test ./cmd/janusfs -run 'TestPrintMountListingsHuman|TestMountListingsJSONShape' -count=1
```

Expected: compile failure because `printMountListings` does not exist.

- [ ] **Step 7: Implement output and command registration**

In `cmd/janusfs/mounts.go`, add:

```go
func newMountsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "mounts",
		Short: "List active and recorded JanusFS mounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMounts(jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func runMounts(jsonOut bool) error {
	records, _ := config.LoadMounts()
	var live []mountStatus
	if resp, err := callDaemon("mounts", daemonRequest{Cmd: "list"}); err == nil && resp.OK {
		live = resp.Mounts
	}
	listings := classifyMountRecords(live, records, defaultStatDir, mountpointMounted)
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(mountListingsResponse{Mounts: listings})
	}
	printMountListings(os.Stdout, listings)
	return nil
}

func printMountListings(w interface{ Write([]byte) (int, error) }, listings []mountListing) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSOURCE\tMOUNTPOINT\tDASHBOARD")
	for _, m := range listings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.Status, m.Src, m.Mountpoint, m.Dashboard)
	}
	_ = tw.Flush()
}
```

In `cmd/janusfs/main.go`, register the command next to mount/umount:

```go
root.AddCommand(newMountsCmd())
```

- [ ] **Step 8: Verify mount command tests pass**

Run:

```bash
go test ./cmd/janusfs -run 'TestClassifyMountRecords|TestPrintMountListingsHuman|TestMountListingsJSONShape' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 2**

Run:

```bash
git add cmd/janusfs/mounts.go cmd/janusfs/mounts_test.go cmd/janusfs/main.go
git commit -m "feat: add mounts listing command"
```

---

### Task 3: Prune impossible mount records during daemon resume

**Files:**
- Modify: `cmd/janusfs/daemon.go`
- Test: `cmd/janusfs/daemon_test.go`

**Interfaces:**
- Consumes: `config.LoadMounts`, `config.ForgetMount`, `unmountKernel`, `pruneMirrorDirs`, `mountpointMounted`
- Produces: daemon startup/resume removes `missing-src` records and unrecoverable stale records from `mounts.json`.

- [ ] **Step 1: Locate resume function**

Run:

```bash
rg -n "func .*resume|LoadMounts|RecordMount|ForgetMount" cmd/janusfs internal/config
```

Expected: find the daemon resume function in `cmd/janusfs/daemon.go` and registry helpers in `internal/config/config.go`.

- [ ] **Step 2: Write failing missing-source cleanup test**

In `cmd/janusfs/daemon_test.go`, add a focused test around the resume helper. Use existing test seams in the file for `startMountFunc` and temp `HOME`. The test should:

```go
func TestResumePrunesMissingSourceRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	missingSrc := filepath.Join(home, "missing")
	mp := filepath.Join(home, ".janusfs", "mounts", "missing")
	if err := os.MkdirAll(mp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.RecordMount(missingSrc, mp, ""); err != nil {
		t.Fatal(err)
	}

	d := &daemon{base: config.Default(), mounts: map[string]*mountRuntime{}, logger: logging.New("test")}
	d.resume()

	records, err := config.LoadMounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("missing source record should be pruned, got %#v", records)
	}
}
```

Add imports if needed: `github.com/sarathsp06/janusfs/internal/config`, `github.com/sarathsp06/janusfs/internal/logging`.

- [ ] **Step 3: Run failing cleanup test**

Run:

```bash
go test ./cmd/janusfs -run TestResumePrunesMissingSourceRecord -count=1
```

Expected: FAIL because resume currently leaves the record.

- [ ] **Step 4: Implement missing-source pruning in daemon resume**

In the daemon resume loop, before attempting `doMount`, add:

```go
if err := defaultStatDir(rec.Src); err != nil {
	if d.logger != nil {
		d.logger.Warn("forgetting recorded mount with missing source", "src", rec.Src, "mountpoint", rec.Mountpoint, "error", err)
	}
	d.forgetMount(rec.Mountpoint)
	continue
}
```

Use the existing `forgetMount` method so pidfiles, registry, and mirror dirs are cleaned consistently.

- [ ] **Step 5: Verify missing-source cleanup test passes**

Run:

```bash
go test ./cmd/janusfs -run TestResumePrunesMissingSourceRecord -count=1
```

Expected: PASS.

- [ ] **Step 6: Write stale-unrecoverable cleanup test**

Add a test that makes `mountpointMounted` return true for the recorded mountpoint, makes `unmountKernel` return nil, and makes `startMountFunc` return an error. Assert the record is removed. Follow existing package-level seam patterns in `umount_test.go` and `daemon_test.go`.

Test skeleton:

```go
func TestResumePrunesUnrecoverableStaleRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(home, "src")
	mp := filepath.Join(home, ".janusfs", "mounts", "src")
	if err := os.MkdirAll(src, 0o755); err != nil { t.Fatal(err) }
	if err := os.MkdirAll(mp, 0o755); err != nil { t.Fatal(err) }
	if err := config.RecordMount(src, mp, ""); err != nil { t.Fatal(err) }

	origMounted := mountpointMounted
	origUnmount := unmountKernel
	origStart := startMountFunc
	defer func() { mountpointMounted = origMounted; unmountKernel = origUnmount; startMountFunc = origStart }()
	mountpointMounted = func(path string) bool { return path == mp }
	unmountKernel = func(path string, force bool) error { return nil }
	startMountFunc = func(context.Context, config.Config, bool) (*mountRuntime, error) {
		return nil, errors.New("resume failed")
	}

	d := &daemon{base: config.Default(), mounts: map[string]*mountRuntime{}, logger: logging.New("test")}
	d.resume()

	records, err := config.LoadMounts()
	if err != nil { t.Fatal(err) }
	if len(records) != 0 {
		t.Fatalf("unrecoverable stale record should be pruned, got %#v", records)
	}
}
```

- [ ] **Step 7: Run failing stale cleanup test**

Run:

```bash
go test ./cmd/janusfs -run TestResumePrunesUnrecoverableStaleRecord -count=1
```

Expected: FAIL until daemon resume removes failed resume records.

- [ ] **Step 8: Implement unrecoverable stale cleanup**

In daemon resume, when a resume mount attempt returns an error after stale cleanup, call:

```go
d.forgetMount(rec.Mountpoint)
```

Log a warning with `src`, `mountpoint`, and `error`. Do not return from daemon startup.

- [ ] **Step 9: Verify daemon cleanup tests pass**

Run:

```bash
go test ./cmd/janusfs -run 'TestResumePrunes(MissingSourceRecord|UnrecoverableStaleRecord)' -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit Task 3**

Run:

```bash
git add cmd/janusfs/daemon.go cmd/janusfs/daemon_test.go
git commit -m "fix: prune stale mount records on daemon resume"
```

---

### Task 4: Update README, SPEC, and knowledge docs

**Files:**
- Modify: `README.md`
- Modify: `SPEC.md`
- Modify: `docs/knowledge/cli-and-daemon.md`
- Modify: `docs/knowledge/log.md`

**Interfaces:**
- Consumes: implemented `janusfs mounts`, `janusfs mounts --json`, `janusfs check --matches`
- Produces: docs that match shipped behavior.

- [ ] **Step 1: Update README command table**

Add/adjust entries:

```markdown
| `janusfs mounts [--json]` | List active daemon mounts plus recorded mount entries, including `mounted`, `recorded`, `missing-src`, `stale`, and `error` status. |
| `janusfs paths` | Show JanusFS config/data paths such as settings, mount registry, global policy, and mount root. |
| `janusfs check [path]` | Static linter for policy mistakes. Add `--matches` to also list files/directories currently resolving Hidden or Masked; `--json` includes matches when requested. |
```

- [ ] **Step 2: Update SPEC CLI requirements**

Add functional requirements for:

```markdown
- `janusfs mounts [--json]` lists active and recorded mounts with status.
- Daemon resume prunes records whose source no longer exists and unrecoverable stale records.
- `janusfs check --matches` previews Hidden/Masked policy matches without changing default lint-only output.
```

- [ ] **Step 3: Update knowledge docs and log**

In `docs/knowledge/cli-and-daemon.md`, document `mounts` and clarify `paths`.

Append to `docs/knowledge/log.md`:

```markdown
* **Mount UX update**: Added `janusfs mounts [--json]`, daemon resume cleanup for missing/stale mount records, and `janusfs check --matches` for Hidden/Masked policy previews. README and SPEC now distinguish mount listing from `janusfs paths`, which remains config/data path diagnostics.
```

- [ ] **Step 4: Verify docs mention implemented commands**

Run:

```bash
rg -n "janusfs mounts|--matches|missing-src|stale" README.md SPEC.md docs/knowledge/cli-and-daemon.md docs/knowledge/log.md
```

Expected: all four files contain relevant references.

- [ ] **Step 5: Commit Task 4**

Run:

```bash
git add README.md SPEC.md docs/knowledge/cli-and-daemon.md docs/knowledge/log.md
git commit -m "docs: document mounts and check matches"
```

---

### Task 5: Final verification

**Files:**
- No code changes expected.

**Interfaces:**
- Verifies all tasks together.

- [ ] **Step 1: Run full tests**

Run:

```bash
rtk make test
```

Expected: `go test ./...` passes.

- [ ] **Step 2: Check git status for unrelated root policy changes**

Run:

```bash
git status --short
```

Expected: implementation files are clean. It is acceptable if pre-existing uncommitted root policy migration remains:

```text
 D .janusignore
 D .janusmask
?? .janusfs.yml
```

Do not stage those unless explicitly asked.

- [ ] **Step 3: Commit only if verification produces cleanup changes**

If formatting/docs changed during verification:

```bash
git add <changed implementation/doc files only>
git commit -m "chore: verify mounts cleanup changes"
```

Otherwise, no commit is needed.

## Self-review

- Spec coverage: daemon registry cleanup is Task 3; `janusfs mounts` is Task 2; `janusfs check --matches` is Task 1; docs are Task 4; verification is Task 5.
- Placeholder scan: no TBD/TODO placeholders remain; tests and commands are explicit.
- Type consistency: `check.Options.Matches`, `check.Report.Matches`, `check.Match`, `mountListing`, and `classifyMountRecords` names are consistent across tasks.
