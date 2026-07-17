# Development Log

Running notes on things that took real investigation, so they aren't re-derived from scratch. Newest entries first.

## 2026-07-16 — Go toolchain hangs on this machine (environment blocker, not code)

**Symptom:** every invocation of `go` (even `go version`, `go help` — no project/network involved) hangs indefinitely. Not slow: zero CPU, zero progress, forever.

**Diagnosis chain:**
1. First hypothesis (mine): system CPU contention from Spotlight re-indexing after the module download/directory scaffold. **Ruled out** — `sample` showed 0% CPU usage on the stuck process, not a busy-but-slow one.
2. Second hypothesis (background subagent's, more precise): `sample` on a hung `go` process showed 100% of samples at `_dyld_start` — i.e. stuck in the dynamic linker before `go`'s own `main()` even begins. Suggested a Gatekeeper/AMFI/`trustd` code-signature check blocked on a silently-dropped network call (suspected Little Snitch, which was running).
3. **Little Snitch disabled** and retested: still hangs, both inside this tool's sandboxed shell and in a plain Terminal.app (confirmed system-wide, not sandbox-specific). Rules out Little Snitch as the (sole/root) cause.
4. **Fresh, decisive `sample` capture** (2026-07-16 22:12, macOS 26.5.2):
   ```
   Call graph:
       7930 Thread_4732269: Main Thread   DispatchQueue_<multiple>
         7930 _dyld_start  (in dyld) + 0  [0x105d809c0]
   ```
   **All 7930 samples sit at `_dyld_start` offset 0** over ~32 seconds of wall-clock (launched 22:11:52, sampled 22:12:24). Not one further instruction has executed. This rules out a stall *inside* dyld's own work (shared-cache mapping, `Security.framework` calls, etc. would show deeper/varied frames) — the process is being held **before dyld runs at all**.
5. This pattern is the signature of the **kernel refusing to let the process past exec**, most consistent with an **Endpoint Security framework client** (`ES_EVENT_TYPE_AUTH_EXEC`) that intercepts process launch and is failing to return a verdict — typically because *its own* cloud reputation-check call is stuck. This is a different mechanism from Gatekeeper/notarization and from any firewall (Little Snitch included), which is why disabling Little Snitch didn't help.
6. Checked for MDM enrollment (`profiles status -type enrollment`): **not enrolled** (`Enrolled via DEP: No`, `MDM enrollment: No`). So if an EDR/endpoint-security agent is the cause, it's not MDM-pushed — it would have been installed some other way (manually, via a prior employer/IT policy before de-enrollment, bundled with other security software, etc.).

**Ruled out:** project code, Little Snitch, Gatekeeper/notarization network check specifically, CPU/Spotlight contention, MDM-pushed policy.

**Not yet checked (next steps after restart):**
- `ls /Library/Application\ Support/ | grep -iE "crowdstrike|sentinelone|jamf|defender|carbonblack|cortex"` and `ps aux | grep -iE "falcon|sentinel|defender|jamf|cbdefense|cortex"` — look for an EDR agent by name.
- Console.app around the timestamp of a hang, searching for `go`/`exec`/any EDR name found above.
- Whether a plain **restart** resolves it outright (a stuck daemon backing the ES client would restart clean) — this is what's being tested right now.
- If restart doesn't fix it: `sudo log stream --style syslog --predicate 'eventMessage contains "exec"'` while reproducing, or checking `system_profiler SPConfigurationProfileDataType` for any leftover security profile even without MDM enrollment.

**Impact on JanusFS work:** nothing written so far has been compiled or tested. All Phase 0 code (`internal/config`, `internal/logging`, `internal/apperrors`, `internal/platform`, `internal/mount`'s passthrough FUSE adapter) was hand-verified against source (jacobsa/fuse's actual API, read directly from the module cache) but needs `gofmt`/`go vet`/`go test`/`go mod tidy` once `go` runs again. `go.mod` has a manually-added, unverified placeholder version for `github.com/caarlos0/env/v11` (see `docs/SPEC_AMENDMENTS.md` 2026-07-16) that needs `go mod tidy` to resolve properly.

**Resume point:** after restart, run `go version` in Terminal.app first. If it works, come back and we run `go mod tidy && gofmt -l . && go vet ./... && go test ./...` from `/Users/sarathsadasivanpillai/projects/safefs` to verify everything written in this session. If it still hangs, work through the "not yet checked" list above.
