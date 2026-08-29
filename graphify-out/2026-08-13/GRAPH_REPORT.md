# Graph Report - janusfs  (2026-08-13)

## Corpus Check
- 174 files · ~214,642 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 1844 nodes · 4309 edges · 139 communities (92 shown, 47 thin omitted)
- Extraction: 84% EXTRACTED · 15% INFERRED · 0% AMBIGUOUS · INFERRED: 666 edges (avg confidence: 0.77)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `1fa3541f`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- 5. Process and package layout
- JanusNode
- rules_test.go
- Pattern
- check/check.go
- Context
- mode-javascript.js
- Path-parity problem
- daemon_test.go
- captureStdout
- cm.js
- NewRecorder
- Run
- pidfile_test.go
- Engine
- MemRegistry
- rr
- Store
- F
- Recorder
- daemon
- newRootCmd
- ResponseWriter
- Server
- Architecture
- ui.go
- Default
- mountForTest
- RecordMount
- Report
- Order and gating sequence
- provider.RamCache (redacted bytes, LRU)
- callDaemon
- ti
- GoReleaser release pipeline
- mode-clike.js
- RuleSet
- P
- M
- W
- mode-xml.js
- addon-search-main.js
- support_linux.go
- mountRuntime
- procid_linux.go
- mode-markdown.js
- PRP 09 — macOS Seatbelt confinement for `janusfs exec`
- Command
- SetOutput
- Registry
- mode-css.js
- config.go
- revocableHandle
- Prometheus-only Observability Design
- daemonRequest
- runNSMount
- addon-search.js
- cr
- macOS Seatbelt enforcement — feasibility spike
- isolation_linux_test.go
- JanusFS Architecture SVG
- mode-python.js
- mode-sql.js
- index.md
- JanusFS Filesystem Boundary Illustration
- Observability event path
- daemonResponse
- Level
- addon-matchbrackets.js
- Knowledge Bundle Update Log
- control_test.go
- benchReadFile
- bench_test.go
- mode-shell.js
- Mask rules with patterns
- Call
- Mounts Cleanup and Check Matches Implementation Plan
- Go Toolchain Hang Investigation
- Run
- procid_darwin.go
- run_spike.sh
- registerPlatformCommands
- GoReleaser build and release
- Length-preserving redaction
- Operation matrix
- Dead code audit methodology
- Quick Commands
- Release Process
- Test Nuances
- FUSE-T spike acceptance list (SPEC §6/§24)
- JanusFS daemon (cmd/janusfs)
- FR-20 Watcher killed
- RTK Commands by Workflow
- Token Savings Overview
- mountStatus
- janusfs daemon
- Package dependency rule
- Config struct
- ponytail: shortcut comments
- Symlink escape check
- readdir inode-zeroing cost
- Rejected isolation ideas
- Writer
- Time
- Regexp
- Mutex
- Uint64
- Decision
- Context
- daemonRequest
- daemonResponse
- github.com/sarathsp06/janusfs
- Case-folding glob evasion
- FUSE adapter thin layer
- Known gaps register
- FR-35 Watchdog subprocess
- FR-39/40 Obs registry
- FR-41/42/43 Dashboard
- FR-45 Virtual .janusfs files
- FR-48–52 CLI commands
- FR-58 Accepted risk (time-range panels)
- docs/janus_art.png oversized asset
- CLI Reference
- Settings Configuration File
- Demo Service

## God Nodes (most connected - your core abstractions)
1. `p()` - 39 edges
2. `P()` - 37 edges
3. `JanusNode` - 32 edges
4. `s()` - 31 edges
5. `Server` - 29 edges
6. `ti()` - 29 edges
7. `F()` - 28 edges
8. `W()` - 27 edges
9. `5. Process and package layout` - 26 edges
10. `Pattern` - 25 edges

## Surprising Connections (you probably didn't know these)
- `pickCheckDir()` --calls--> `New()`  [INFERRED]
  cmd/janusfs/check.go → internal/api/server.go
- `runCheck()` --calls--> `RunWithOptions()`  [INFERRED]
  cmd/janusfs/check.go → internal/check/check.go
- `runInitGlobal()` --calls--> `GlobalDir()`  [INFERRED]
  cmd/janusfs/init.go → internal/rules/rules.go
- `callDaemon()` --calls--> `Call()`  [INFERRED]
  cmd/janusfs/mount.go → internal/control/control.go
- `runMountsPick()` --calls--> `New()`  [INFERRED]
  cmd/janusfs/mounts.go → internal/api/server.go

## Import Cycles
- None detected.

## Hyperedges (group relationships)
- **Filesystem Boundary Illustration Components** — docs_janus_art_png_janus, docs_janus_art_png_filesystem_boundary, docs_janus_art_png_trusted_source, docs_janus_art_png_untrusted_agent, docs_janus_art_png_three_faces, docs_janus_art_png_policy_enforcement [AMBIGUOUS 0.30]
- **Core read-path subsystems** — docs_knowledge_fuse_adapter_janus_node, docs_knowledge_policy_engine_resolution, docs_knowledge_masking_pipeline_ramcache, docs_knowledge_masking_pipeline_contentkey, docs_knowledge_fuse_adapter_masked_handle [EXTRACTED 1.00]
- **Platform isolation design family** — docs_knowledge_platform_isolation_linux_namespace_exec, docs_knowledge_platform_isolation_macos_path_preserving, docs_knowledge_process_identity_registry, docs_knowledge_exec_and_path_parity_path_parity_problem, PRPs_04_linux_namespace_exec_prp04, PRPs_07_macos_path_preserving_prp07 [EXTRACTED 1.00]
- **Correctness hardening batch** — PRPs_01_correctness_fixes_prp01, PRPs_02_crash_recovery_watchdog_prp02, PRPs_03_decision_cache_prp03, PRPs_08_reload_revocation_prp08 [EXTRACTED 1.00]
- **Prometheus-only observability workstream** — docs_superpowers_plans_2026-07-23_prometheus_only_obs, docs_superpowers_specs_2026-07-23_prometheus_only_obs_design, internal_ui_index_html, concept_internal_obs_recorder, concept_prometheus_metrics_surface [INFERRED 0.85]
- **Mounts cleanup and check matches workstream** — docs_superpowers_plans_2026-07-29_mounts_cleanup_and_check_matches, docs_superpowers_specs_2026-07-29_mounts_cleanup_and_check_matches_design, concept_mount_registry_cleanup, concept_check_matches, concept_janusfs_daemon [INFERRED 0.85]
- **Performance optimization learnings and baselines** — jules_bolt, jules_prune, bench_baseline, concept_regexp_optimization, concept_slices_sortfunc, concept_nfr_3_performance_budget [INFERRED 0.85]

## Communities (139 total, 47 thin omitted)

### Community 0 - "5. Process and package layout"
Cohesion: 0.05
Nodes (81): Agent session, 9. Backing access layer, Cache isolation: direct I/O + zero timeouts for real files (FR-34), cmd/janusfs (entrypoint, DI, cobra), Concurrent re-redaction: serve previous bytes or block (FR-23), 16. Configuration, logging, and process wiring, Crash recovery: detached supervisor polls then force-unmounts (FR-35), Decision (ALLOWED | MASKED | HIDDEN) (+73 more)

### Community 1 - "JanusNode"
Cohesion: 0.06
Nodes (58): Root, InodeEmbedder, Errno, T, TestToErrnoMapsEachSentinel(), TestToErrnoMatchesWrappedSentinels(), ToErrno(), Open() (+50 more)

### Community 2 - "rules_test.go"
Cohesion: 0.08
Nodes (58): caseInsensitiveVolume(), flipCase(), compilePatternFold(), Regexp, gitCheckIgnore(), T, runGit(), TestGitConformance() (+50 more)

### Community 3 - "Pattern"
Cohesion: 0.05
Nodes (89): Buffer, Element, T, TestVirtualDirUnit(), Builtins(), containsIgnoreCase(), getBuiltinPreFilter(), init() (+81 more)

### Community 4 - "check/check.go"
Cohesion: 0.11
Nodes (49): Finding, levelInfo, Match, matcher, Options, Report, Severity, treeEntry (+41 more)

### Community 5 - "Context"
Cohesion: 0.15
Nodes (16): AttrOut, Context, DirStream, EntryOut, Errno, FileHandle, Inode, ReadResult (+8 more)

### Community 6 - "mode-javascript.js"
Cohesion: 0.16
Nodes (53): Le(), Me(), I(), a(), ae(), b(), be(), C() (+45 more)

### Community 7 - "Path-parity problem"
Cohesion: 0.06
Nodes (52): PRP 01 Correctness fixes, PRP 02 Crash recovery watchdog, PRP 03 Decision cache, PRP 04 Linux namespace exec, PRP 05 Dirfd backing layer, PRP 06 Process identity, PRP 07 macOS path-preserving, PRP 08 Reload revocation (+44 more)

### Community 8 - "daemon_test.go"
Cohesion: 0.16
Nodes (26): fakeRuntime(), T, TestBrowserOpenCommandByPlatform(), TestChildMountsUnder(), TestDaemonCall_NoDaemon(), TestDaemonIndex_FallsBackToSrcWithoutLabel(), TestDaemonIndex_NotFoundForOtherPaths(), TestDaemonIndex_RendersLabelAndEscapes() (+18 more)

### Community 9 - "captureStdout"
Cohesion: 0.11
Nodes (40): Command, newCheckCmd(), pickCheckDir(), printCheckMatches(), runCheck(), appendPolicyFixture(), captureStdout(), T (+32 more)

### Community 10 - "cm.js"
Cohesion: 0.09
Nodes (26): ao(), At(), Bo(), br(), ct(), dt(), el(), fe() (+18 more)

### Community 11 - "NewRecorder"
Cohesion: 0.33
Nodes (19): T, TestAuthMissing(), TestAuthQueryParam(), TestConfigSaveTriggersReload(), TestHeaders(), TestHistoryEndpointNoStore(), TestHostSecurity(), TestLatencyEndpointRemoved() (+11 more)

### Community 12 - "Run"
Cohesion: 0.08
Nodes (41): newExecCmd(), T, runExecArgs(), TestExecFlagParsing(), Command, Context, daemonResponse, isNameChar() (+33 more)

### Community 13 - "pidfile_test.go"
Cohesion: 0.25
Nodes (20): pidAlive(), pidfilePath(), pruneMirrorDirs(), readPidfile(), removePidfile(), T, TestPidAlive(), TestPidfilePath_DiffersForDifferentMountpoints() (+12 more)

### Community 14 - "Engine"
Cohesion: 0.12
Nodes (32): decisionKey, dirConfigFP, Engine, Resolution, Int64, buildConfigSnapshot(), Decision, RuleSet (+24 more)

### Community 15 - "MemRegistry"
Cohesion: 0.11
Nodes (24): classify(), isAgent(), T, scrubEnv(), startChild(), TestCacheHitCounter(), TestEnvironSelfReturnsSomething(), TestIsAgentAncestryWalk() (+16 more)

### Community 16 - "rr"
Cohesion: 0.15
Nodes (21): ae(), Bn(), bt(), er(), go(), he(), ir(), jo() (+13 more)

### Community 17 - "Store"
Cohesion: 0.13
Nodes (20): DB, OpRollup, opRow, Store, Context, Duration, Mutex, Time (+12 more)

### Community 18 - "F"
Cohesion: 0.15
Nodes (25): bi(), Di(), Ei(), eo(), F(), Fi(), gi(), io() (+17 more)

### Community 19 - "Recorder"
Cohesion: 0.10
Nodes (18): Counter, CounterVec, Gauge, HistogramVec, formatBytes(), Time, Decision, knownDecisions() (+10 more)

### Community 20 - "daemon"
Cohesion: 0.16
Nodes (13): browserOpenCommand(), Command, Conn, Context, daemonRequest, daemonResponse, daemon, Logger (+5 more)

### Community 21 - "newRootCmd"
Cohesion: 0.15
Nodes (18): expandHome(), Command, newInstallCmd(), promptWithDefault(), runInstall(), Command, main(), newRootCmd() (+10 more)

### Community 23 - "Server"
Cohesion: 0.15
Nodes (16): Server, VFSStats, FS, Handler, HandlerFunc, New(), relativeRuleRef(), withHeaders() (+8 more)

### Community 24 - "Architecture"
Cohesion: 0.12
Nodes (24): Architecture, Code Conventions, Packages, Working with SPEC.md, Quick Reference Commands, Formatting and Linting, Assets, Leak Channels (+16 more)

### Community 25 - "ui.go"
Cohesion: 0.06
Nodes (67): Report, printCheckReport(), Command, Report, newDoctorCmd(), printDoctorReport(), Command, newInitCmd() (+59 more)

### Community 26 - "Default"
Cohesion: 0.24
Nodes (22): Default(), T, newCfg(), TestApplyEnv_DoesNotTouchPositionals(), TestApplyEnv_LeavesUnsetFieldsAtDefault(), TestApplyEnv_NoHistoryBoolFromEnv(), TestApplyEnv_OverridesDefaultWhenSet(), TestApplyFile_MissingKeepsDefaultMountRoot() (+14 more)

### Community 27 - "mountForTest"
Cohesion: 0.21
Nodes (19): T, TestCreateGating(), TestLinkDeniesLaunderingMaskedFile(), TestListxattrGating(), TestMaskedXattrSideChannel(), TestReloadTakesEffectWithoutRemount(), TestVirtualDir(), appendPolicyFixture() (+11 more)

### Community 28 - "RecordMount"
Cohesion: 0.19
Nodes (19): Command, Context, Duration, Logger, newWatchdogCmd(), runWatchdog(), spawnWatchdog(), stopWatchdog() (+11 more)

### Community 29 - "Report"
Cohesion: 0.16
Nodes (15): MacFUSEStatus, MountInfo, Report, RuntimeInfo, WatchdogStatus, checkMacFUSE(), checkMacFUSE(), Run() (+7 more)

### Community 30 - "Order and gating sequence"
Cohesion: 0.15
Nodes (21): Daemon watchdog subcommand, Hardlink escape prevention, Process identity (Tier 2), Order and gating sequence, Platform isolation model, Product Requirement Prompt (PRP), PRP-01 Correctness fixes, PRP-02 Crash recovery watchdog (+13 more)

### Community 31 - "provider.RamCache (redacted bytes, LRU)"
Cohesion: 0.13
Nodes (20): api.Server (per-mount dashboard handler), internal/apperrors (errno mapping), janusfs CLI clients (mount|umount|update|path), ContentKey (path,mtime,size,inode,gen), ~/.janusfs/daemon.sock control socket, Dashboard HTTP (127.0.0.1:7381), Decision (HIDDEN > MASKED > ALLOWED), engine.Engine (atomic rule snapshot) (+12 more)

### Community 32 - "callDaemon"
Cohesion: 0.23
Nodes (11): callDaemon(), Command, Logger, historyDBPath(), logLevel(), newMountCmd(), newUpdateCmd(), runMount() (+3 more)

### Community 33 - "ti"
Cohesion: 0.15
Nodes (27): an(), ce(), ci(), Cn(), i(), ie(), J(), jr() (+19 more)

### Community 34 - "GoReleaser release pipeline"
Cohesion: 0.11
Nodes (18): CGO_ENABLED=0 policy, Conventional Commits changelog, SHA256 checksums, GitHub Releases, Homebrew tap publication, NFR-7 Single static binary, GoReleaser release pipeline, CycloneDX SBOM via syft (+10 more)

### Community 35 - "mode-clike.js"
Cohesion: 0.16
Nodes (10): C(), E(), F(), h(), L(), m(), N(), s() (+2 more)

### Community 36 - "RuleSet"
Cohesion: 0.23
Nodes (7): Decision, relativeToLevel(), IgnoreLevel, MaskLevel, Resolution, RuleSet, TraceEntry

### Community 37 - "P"
Cohesion: 0.13
Nodes (31): A(), al(), cl(), D(), de(), dl(), $e(), fl() (+23 more)

### Community 38 - "M"
Cohesion: 0.16
Nodes (22): ai(), dn(), en(), fn(), H(), hi(), ii(), M() (+14 more)

### Community 39 - "W"
Cohesion: 0.23
Nodes (12): b(), co(), je(), jn(), kl(), Ll(), ol(), Sl() (+4 more)

### Community 40 - "mode-xml.js"
Cohesion: 0.22
Nodes (13): b(), c(), g(), h(), k(), m(), N(), o() (+5 more)

### Community 41 - "addon-search-main.js"
Cohesion: 0.33
Nodes (14): a(), b(), C(), d(), g(), h(), m(), n() (+6 more)

### Community 42 - "support_linux.go"
Cohesion: 0.23
Nodes (11): leadingDigits(), nullTerminatedString(), parseKernelVersion(), T, TestNullTerminatedString(), TestParseKernelVersion(), checkDevFuse(), checkKernelVersion() (+3 more)

### Community 43 - "mountRuntime"
Cohesion: 0.17
Nodes (11): CancelFunc, Context, Logger, makeObserver(), startMount(), Engine, Context, Logger (+3 more)

### Community 44 - "procid_linux.go"
Cohesion: 0.26
Nodes (10): parent(), parentAndStartTime(), parseParentAndStartTime(), parseStatFields(), readStatFields(), startTime(), T, TestParseParentAndStartTimeHandlesCommWithSpacesAndParens() (+2 more)

### Community 45 - "mode-markdown.js"
Cohesion: 0.32
Nodes (11): a(), B(), C(), e(), L(), M(), o(), q() (+3 more)

### Community 46 - "PRP 09 — macOS Seatbelt confinement for `janusfs exec`"
Cohesion: 0.15
Nodes (12): Anti-patterns, Context, Design, Done when, Goal, If this is wrong, PRP 09 — macOS Seatbelt confinement for `janusfs exec`, Related / follow-on PRPs (scoped, not part of this branch) (+4 more)

### Community 48 - "SetOutput"
Cohesion: 0.27
Nodes (9): Logger, Writer, New(), SetOutput(), T, TestNewConcurrentWithSetOutputIsSafe(), TestNewProducesValidJSONWithComponent(), TestSetOutputLevelRespected() (+1 more)

### Community 49 - "Registry"
Cohesion: 0.38
Nodes (11): assertMetric(), T, labelsMatch(), metricValue(), newBlockingSink(), TestRecorderDoesNotUsePathLabels(), TestRecorderEmitsPrometheusMetrics(), TestRecorderHistoryFanoutDropsInsteadOfBlocking() (+3 more)

### Community 50 - "mode-css.js"
Cohesion: 0.20
Nodes (4): C(), q(), x(), z()

### Community 51 - "config.go"
Cohesion: 0.20
Nodes (16): pruneStaleRegistry(), Config, fileSettings, MountRecord, absClean(), ApplyEnv(), envBool(), envInt() (+8 more)

### Community 52 - "revocableHandle"
Cohesion: 0.27
Nodes (6): Context, Errno, ReadResult, Time, LoopbackFile, revocableHandle

### Community 53 - "Prometheus-only Observability Design"
Cohesion: 0.24
Nodes (10): Phase 0 Baseline — NFR-3 performance budgets, internal/obs Recorder with Prometheus native collectors, NFR-3 performance budget thresholds, Prometheus /metrics as single metrics surface, Regexp pre-filtering and allocation bypass, slices.SortFunc zero-allocation sorting, Prometheus-only Observability Implementation Plan, Prometheus-only Observability Design (+2 more)

### Community 55 - "runNSMount"
Cohesion: 0.29
Nodes (8): Command, Context, Level, Logger, newNSMountCmd(), registerPlatformCommands(), runNSMount(), nsmountLogWriter

### Community 56 - "addon-search.js"
Cohesion: 0.49
Nodes (9): a(), c(), d(), h(), i(), L(), m(), o() (+1 more)

### Community 57 - "cr"
Cohesion: 0.22
Nodes (14): ar(), cr(), dr(), fr(), gn(), gr(), Hn(), hr() (+6 more)

### Community 58 - "macOS Seatbelt enforcement — feasibility spike"
Cohesion: 0.22
Nodes (8): Gotchas (both fail **open** — a wrong profile silently allows everything), macOS Seatbelt enforcement — feasibility spike, Open risks — not yet tested, Recommendation (implemented — see status line above), Results, The `.git` write-back caveat (applies to every enforced-view design, not just Seatbelt), Validated profile, Why this matters

### Community 59 - "isolation_linux_test.go"
Cohesion: 0.67
Nodes (8): buildJanusfsBinary(), T, readMountinfo(), skipIfUnsupportedPrivateMount(), TestNamespaceIsolation_ExitCodeAndSignals(), TestNamespaceIsolation_HostMountTableUnaffected(), TestNamespaceIsolation_NoDaemonRequired(), TestNamespaceIsolation_TeardownRestoresNormalAccess()

### Community 60 - "JanusFS Architecture SVG"
Cohesion: 0.46
Nodes (8): Agent (untrusted), ALLOWED → passthrough, HIDDEN → deny (EACCES), JanusFS Architecture SVG, JanusFS (FUSE mount), MASKED → redaction (RAM), Policy snapshot (.janusignore / .janusmask), Underlying real files on disk (trusted)

### Community 61 - "mode-python.js"
Cohesion: 0.54
Nodes (6): b(), g(), h(), i(), w(), y()

### Community 63 - "index.md"
Cohesion: 0.33
Nodes (5): Also in this repo, Isolation (the current design frontier), Start here, The control plane, The read path (how a byte gets decided)

### Community 64 - "JanusFS Filesystem Boundary Illustration"
Cohesion: 0.52
Nodes (7): JanusFS Filesystem Boundary Illustration, Filesystem Boundary, Janus (Two-Faced God of Doorways), Policy Enforcement at the Filesystem Boundary, Three Faces of Access (Allowed, Masked, Hidden), Trusted Source Code Side, Untrusted AI Agent Side

### Community 65 - "Observability event path"
Cohesion: 0.29
Nodes (7): mountRuntime, Dashboard multiplexing, ~/.janusfs directory, Observability event path, History SQLite store, makeObserver, Non-blocking observability

### Community 68 - "addon-matchbrackets.js"
Cohesion: 0.52
Nodes (5): e(), f(), i(), o(), y()

### Community 69 - "Knowledge Bundle Update Log"
Cohesion: 0.33
Nodes (5): 2026-07-26, 2026-07-26 (later the same day), 2026-07-27, 2026-08-03, Knowledge Bundle Update Log

### Community 72 - "control_test.go"
Cohesion: 0.60
Nodes (4): T, TestRequestResumeNeverSerializes(), TestRequestRoundTrip(), TestWriteResponseDefaultsOKFromError()

### Community 73 - "benchReadFile"
Cohesion: 0.80
Nodes (4): BenchmarkHostRead_NoActiveExecSession(), BenchmarkHostRead_WithActiveExecSession(), benchReadFile(), B

### Community 74 - "bench_test.go"
Cohesion: 0.60
Nodes (4): BenchmarkAncestryWalk(), BenchmarkIsAgentCacheHit(), BenchmarkStartTime(), B

### Community 75 - "mode-shell.js"
Cohesion: 0.80
Nodes (4): e(), f(), i(), l()

### Community 76 - "Mask rules with patterns"
Cohesion: 0.50
Nodes (5): env-value redaction pattern, Hide rules, Mask rules with patterns, JanusFS policy version 1, Secret pattern library

### Community 77 - "Call"
Cohesion: 0.20
Nodes (13): daemonLogPath(), Command, newLogsCmd(), startDaemonBackground(), daemon, ResponseWriter, MountStatus, Request (+5 more)

### Community 78 - "Mounts Cleanup and Check Matches Implementation Plan"
Cohesion: 0.50
Nodes (4): janusfs check --matches policy preview, Mount registry self-healing on daemon resume, Mounts Cleanup and Check Matches Implementation Plan, Mount Registry Cleanup and Check Matches Design

### Community 79 - "Go Toolchain Hang Investigation"
Cohesion: 0.50
Nodes (4): Diagnostic Chain, Go Toolchain Hang Investigation, Impact on JanusFS Work, Build Requirements

### Community 80 - "Run"
Cohesion: 0.67
Nodes (3): discoverSourceRoot(), Context, Run()

### Community 81 - "procid_darwin.go"
Cohesion: 0.83
Nodes (3): parent(), parentAndStartTime(), startTime()

### Community 84 - "GoReleaser build and release"
Cohesion: 0.67
Nodes (3): GoReleaser build and release, .github/workflows/ci.yml — CI pipeline, .github/workflows/release.yml — Release pipeline

### Community 85 - "Length-preserving redaction"
Cohesion: 0.67
Nodes (3): Leak oracle, Length-preserving redaction, Redaction streaming modes

### Community 86 - "Operation matrix"
Cohesion: 0.67
Nodes (3): gate() decision-to-errno, Operation matrix, xattr redaction side channel

### Community 87 - "Dead code audit methodology"
Cohesion: 0.67
Nodes (3): Dead code audit methodology, Scaffolding traps vs genuine dead code, API standalone-serve dead code cluster

## Ambiguous Edges - Review These
- `JanusFS Filesystem Boundary Illustration` → `Policy Enforcement at the Filesystem Boundary`  [AMBIGUOUS]
  docs/janus_art.png · relation: references
- `JanusFS Filesystem Boundary Illustration` → `Three Faces of Access (Allowed, Masked, Hidden)`  [AMBIGUOUS]
  docs/janus_art.png · relation: references

## Knowledge Gaps
- **114 isolated node(s):** `unmountAttempt`, `github.com/sarathsp06/janusfs`, `builtinSpec`, `run_spike.sh script`, `fileSettings` (+109 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **47 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **What is the exact relationship between `JanusFS Filesystem Boundary Illustration` and `Policy Enforcement at the Filesystem Boundary`?**
  _Edge tagged AMBIGUOUS (relation: references) - confidence is low._
- **What is the exact relationship between `JanusFS Filesystem Boundary Illustration` and `Three Faces of Access (Allowed, Masked, Hidden)`?**
  _Edge tagged AMBIGUOUS (relation: references) - confidence is low._
- **Why does `mountRuntime` connect `mountRuntime` to `Pattern`, `daemon_test.go`, `Engine`, `Store`, `Recorder`, `daemon`, `Server`?**
  _High betweenness centrality (0.078) - this node is a cross-community bridge._
- **Why does `JanusRoot` connect `JanusNode` to `Pattern`, `mountRuntime`, `Context`, `Engine`?**
  _High betweenness centrality (0.045) - this node is a cross-community bridge._
- **Why does `Engine` connect `Engine` to `JanusNode`, `mountRuntime`, `check/check.go`?**
  _High betweenness centrality (0.041) - this node is a cross-community bridge._
- **Are the 3 inferred relationships involving `P()` (e.g. with `al()` and `il()`) actually correct?**
  _`P()` has 3 INFERRED edges - model-reasoned connections that need verification._
- **Are the 2 inferred relationships involving `s()` (e.g. with `E()` and `T()`) actually correct?**
  _`s()` has 2 INFERRED edges - model-reasoned connections that need verification._