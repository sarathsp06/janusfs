# PRP 01 — Correctness fixes

**Size** S · **Blocked by** nothing · **Touches** `internal/mount`, `internal/rules`,
`internal/execrunner`, `cmd/janusfs`, new `internal/control`

## Goal

Close two live policy bypasses and three defects that make the tool lie to the
user. Each of the five tasks is independent; land them as five commits on one
branch.

## Why

Tasks 1 and 2 are one-syscall escapes from a mask. They are exploitable today by
any agent with a shell, they need no privileges, and neither is covered by an
existing requirement. Tasks 3–5 are the difference between a tool that reports
honestly and one that quietly does the wrong thing.

## Context

- Adapter overrides and the as-built matrix:
  [`docs/knowledge/fuse-adapter.md`](../docs/knowledge/fuse-adapter.md)
- The defects, with reasoning:
  [`docs/knowledge/known-gaps.md`](../docs/knowledge/known-gaps.md) items 1, 2, 4,
  8, 9
- Requirements: [SPEC.md FR-11](../SPEC.md#32-decision-semantics) (hardlinks),
  [FR-13](../SPEC.md#32-decision-semantics) (path identity),
  [FR-26](../SPEC.md#35-isolation-and-path-parity) (exec refuses to guess),
  [FR-50](../SPEC.md#38-cli-and-diagnostics) (doctor)

Verified API facts you can rely on:

- `fs.NodeLinker` is
  `Link(ctx context.Context, target InodeEmbedder, name string, out *fuse.EntryOut) (*Inode, syscall.Errno)`.
  `target` is an `fs.InodeEmbedder`, so it type-asserts to `*JanusNode`.
- `internal/rules/glob.go` has exactly **one** regex compile site,
  `regexp.Compile(full)` at line ~90. That is the only place case folding needs to
  be applied.

---

## Task 1 — Deny agent-created hardlinks to non-ALLOWED inodes

**File** `internal/mount/janus_node.go:378`

Current code checks only the new link name:

```go
func (n *JanusNode) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	d := n.decisionFor(name)          // ← the NEW name only
	if d != engine.Allowed {
		return nil, syscall.EACCES
	}
	return n.LoopbackNode.Link(ctx, target, name, out)
}
```

The exploit, with `.janusmask` containing `*.env: env-value`:

```
link("secrets.env", "copy.txt")   → decisionFor("copy.txt") == Allowed → permitted
cat copy.txt                      → resolve("copy.txt")     == Allowed → plaintext
```

**Change**: resolve the target's own decision too, and deny unless both are
`Allowed`. A target that is not a `*JanusNode` is denied — it is not a node this
mount governs, so it cannot be established as safe.

```go
tn, ok := target.(*JanusNode)
if !ok {
	n.observe("link", "HIDDEN", 0, start)
	return nil, syscall.EACCES
}
if tn.resolve().Decision != engine.Allowed {
	// observe with the target's decision so the dashboard shows why
	return nil, syscall.EACCES
}
```

Order the checks so the *target* is evaluated before the new name: an attempt to
launder a masked inode is the interesting event to record.

**Do not** change FR-11's accepted behaviour for hardlinks that already exist on
disk. Two pre-existing links to one inode may still carry different decisions;
only *creating* a new one through the mount is denied.

**Test** `internal/mount/janus_virtual_unit_test.go` or a new
`link_test.go`: build a `JanusRoot` over a temp tree with a masked file, call
`Link` with the masked node as target, assert `EACCES`, and assert the link was
not created on disk.

---

## Task 2 — Case-correct glob matching on case-insensitive volumes

**Files** `internal/rules/glob.go`, `internal/rules/rules.go`

`ignorePattern.matches` compiles a case-sensitive regex. APFS and HFS+ are
case-**insensitive** by default, so:

```
cat .ENV
```

resolves the literal string `.ENV` against `*.env`, misses, returns `ALLOWED`,
and serves plaintext — while the kernel happily opened the real `.env` inode.

**Change**, in three parts:

1. Add a volume probe. No writing to the user's tree:

```go
// caseInsensitiveVolume reports whether dir lives on a volume that treats two
// spellings of a name as the same file. Probed by case-flipping dir's own
// basename and checking whether it resolves to the same inode, which needs no
// write access and no platform-specific syscall.
func caseInsensitiveVolume(dir string) bool
```

   Implementation: take `filepath.Base(dir)`, build a variant with its case
   flipped (`strings.ToUpper`, falling back to `ToLower` if the name is already
   upper). If the flipped name equals the original (no cased letters), fall back
   to `runtime.GOOS == "darwin"`. Otherwise `os.Stat` both and compare
   `Sys().(*syscall.Stat_t).Ino`.

2. Store the result once on the compiled rule set: add `FoldCase bool` to
   `RuleSet` (`rules.go:120`), set in `Discover` (`rules.go:133`) from
   `caseInsensitiveVolume(rootAbs)`.

3. Thread it to the single compile site: `compilePattern(lineNo int, raw string, foldCase bool)`,
   prepending `(?i)` to `full` before `regexp.Compile` when set. Callers to
   update: `loadIgnoreLevel` (`rules.go:226`), `parseMaskLine` (`rules.go:276`),
   and the existing tests in `glob_test.go`.

**Tie the behaviour to the volume, not to a global preference.** `!`-negation
lines widen visibility, so a blanket `(?i)` is not uniformly fail-closed — on a
genuinely case-sensitive volume it would make a negation match more paths than
the user wrote.

**Test** in `internal/rules/rules_test.go`: with `FoldCase` forced true, assert
`.ENV` resolves `MASKED` against a `*.env` mask rule; with it false, assert it
resolves `ALLOWED`. Both directions, because the flag has to actually be
load-bearing. Additionally assert `caseInsensitiveVolume(t.TempDir())` returns
true on darwin and does not panic anywhere.

---

## Task 3 — `exec` must refuse to guess a source tree

**File** `internal/execrunner/runner.go:111`

`findSourceAndMount` walks up looking for an active mount or a directory holding
`.janusignore`/`.janusmask`, and when it finds neither it defaults to the cwd:

```go
if foundSrc == "" {
	foundSrc = cwd          // ← provisions an unpoliced mount
}
```

Run `janusfs exec -- claude` from a home directory and it mounts the entire home
tree with an empty policy. That is the opposite of the tool's purpose.

**Change**: return an error with a cause and a remedy.

```
exec: no JanusFS policy found for /Users/me — refusing to mount an unpoliced tree
Remedy: run `janusfs init` here, or in the project root above this directory
```

Keep the exit code `125`, matching the existing not-started convention.

**Test** `internal/execrunner/runner_test.go`: with a temp cwd containing no
policy files and no daemon, assert the error mentions `janusfs init` and that no
mount request was sent.

---

## Task 4 — One home for the control protocol

**Files** new `internal/control/control.go`; edit `cmd/janusfs/daemon.go:30`,
`internal/execrunner/runner.go:18`

`daemonRequest`, `daemonResponse`, and `mountStatus` are declared twice, because
the originals are unexported in `package main`. Adding a field in one place
silently breaks the other.

**Change**: move the three types plus the dial-and-call helper into
`internal/control`:

```go
package control

type Request struct {
	Cmd        string `json:"cmd"`         // "mount" | "unmount" | "list" | "reload"
	Src        string `json:"src,omitempty"`
	Mountpoint string `json:"mountpoint,omitempty"`
	Label      string `json:"label,omitempty"`
	NoHistory  bool   `json:"no_history,omitempty"`

	// Resume is daemon-internal and never crosses the socket: a client must not
	// be able to ask for resume semantics, which may create a mountpoint
	// directory that a fresh mount may not.
	Resume bool `json:"-"`
}

type MountStatus struct { Src, Label, Mountpoint, Dashboard string }
type Response struct { OK bool; Error, Message string; Mounts []MountStatus }

func SocketPath() (string, error)
func Call(req Request) (Response, error)   // wraps ErrDaemonNotRunning
```

Move `errDaemonNotRunning` to `control.ErrDaemonNotRunning` so both callers match
it with `errors.Is`. Keep the `json:"-"` on `Resume` and keep its comment — it is
a security property, not a serialization detail.

**Test** `internal/control/control_test.go`: round-trip a `Request` through
`encoding/json` and assert `Resume` does not appear in the output.

---

## Task 5 — `doctor` must report real mountpoints

**Files** `cmd/janusfs/pidfile.go:36`, `internal/health/doctor.go:78`

`health.Run` sets `MountInfo.Mountpoint` to the pidfile name with `.pid`
stripped. Pidfile names are `sha256(abs mountpoint)` (`pidfile.go:23`), so the
field holds a hash and the path is unrecoverable — the one thing a user needs in
order to act on a stale entry.

**Change**: write the mountpoint into the pidfile as a second line, and read it
back.

```
<pid>\n<absolute mountpoint>\n
```

`writePidfile` gains the mountpoint in its payload. `readPidfile`
(`pidfile.go:50`) must keep parsing **only the first line**, so a pidfile written
by an older build still works — use `strings.SplitN(s, "\n", 2)` and parse
`[0]`. `health.Run` reads line 2 when present and falls back to the hash-derived
placeholder when absent, marking it as unknown rather than presenting a hash as
a path.

**Test** `cmd/janusfs/pidfile_test.go`: write a pidfile, assert `readPidfile`
returns the PID, and assert the second line is the absolute mountpoint. Add a
case with a single-line pidfile to prove backward compatibility.

---

## Validation

```bash
rtk make verify        # fmt-check, vet, test-race
rtk make leak-oracle   # tasks 1 and 2 change what reaches a caller
rtk go test ./internal/rules/... ./internal/mount/... ./internal/control/... -run 'Case|Link|Control' -v
```

## Done when

- [ ] `Link` denies a non-`Allowed` target, with a test that fails without the fix
- [ ] `.ENV` resolves `MASKED` against `*.env` on a case-insensitive volume, and
      `ALLOWED` on a case-sensitive one, both asserted
- [ ] `janusfs exec` in a policy-free directory errors with a remedy and mounts
      nothing
- [ ] The control-protocol types exist in exactly one place, and `Resume` is
      proven unserialized
- [ ] `janusfs doctor` prints a path, never a hash
- [ ] Items 1, 2, 4, 8, 9 removed from
      [`known-gaps.md`](../docs/knowledge/known-gaps.md); a line appended to
      [`log.md`](../docs/knowledge/log.md)

## If this is wrong

- **Task 1** assumes `target` is always a `*JanusNode` in practice. If a real
  workload trips the deny-on-type-mismatch branch, do not relax it — report,
  because it means a node is entering the tree by a path the adapter does not
  construct.
- **Task 2** assumes the inode-comparison probe is reliable. If it reports
  case-sensitive on a default APFS volume, stop: a wrong answer here silently
  reopens the bypass. Fall back to `runtime.GOOS == "darwin"` and report.

## Anti-patterns

- Do not "fix" Task 1 by making decisions inode-keyed. Per-path decisions are a
  deliberate design property, and changing it is a much larger question.
- Do not apply `(?i)` unconditionally to dodge the probe.
- Do not make Task 3 a warning. Refusing is the point.
