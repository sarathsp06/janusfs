# Graph Report - /Users/sarathsadasivanpillai/projects/janusfs  (2026-07-30)

## Corpus Check
- 175 files · ~202,007 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 1746 nodes · 4125 edges · 121 communities (88 shown, 33 thin omitted)
- Extraction: 86% EXTRACTED · 14% INFERRED · 0% AMBIGUOUS · INFERRED: 586 edges (avg confidence: 0.76)
- Token cost: 0 input · 0 output

## Community Hubs (Navigation)
- FUSE File Operations
- Architecture & Design Decisions
- Init & Daemon Commands
- Rule Matching Engine
- Pattern Library
- Diagnostics & Check Reporting
- CLI Check Command
- CodeMirror Vendor JS
- Implementation Plans (PRPs)
- Mount Layer & Provider
- PID & Process Management
- Policy Engine
- Process Identity & Agent Detection
- CodeMirror Core
- HTTP API Server
- CodeMirror Editor
- History Store (SQLite)
- Observability & Metrics
- Daemon Tests
- CodeMirror Modes
- Virtual FUSE Node
- Vendor JS Utilities
- Documentation & Knowledge
- Exec Runner & Path Rewriter
- Control Protocol & Daemon IPC
- Community 25
- Community 26
- Community 27
- Community 28
- Community 29
- Community 30
- Community 31
- Community 32
- Community 33
- Community 34
- Community 35
- Community 36
- Community 37
- Community 38
- Community 39
- Community 40
- Community 41
- Community 42
- Community 43
- Community 44
- Community 45
- Community 46
- Community 47
- Community 48
- Community 49
- Community 50
- Community 51
- Community 52
- Community 53
- Community 54
- Community 55
- Community 56
- Community 57
- Community 58
- Community 59
- Community 62
- Community 63
- Community 64
- Community 65
- Community 66
- Community 67
- Community 68
- Community 69
- Community 70
- Community 71
- Community 72
- Community 73
- Community 74
- Community 75
- Community 76
- Community 77
- Community 78
- Community 79
- Community 80
- Community 81
- Community 82
- Community 83
- Community 84
- Community 88
- Community 90
- Community 91
- Community 94
- Community 95
- Community 96
- Community 97
- Community 98
- Community 99
- Community 100
- Community 107
- Community 108
- Community 109
- Community 110
- Community 111
- Community 112
- Community 113
- Community 114
- Community 115
- Community 116
- Community 117
- Community 118
- Community 119
- Community 120

## God Nodes (most connected - your core abstractions)
1. `p()` - 39 edges
2. `P()` - 37 edges
3. `Server` - 32 edges
4. `JanusNode` - 32 edges
5. `s()` - 31 edges
6. `ti()` - 29 edges
7. `F()` - 28 edges
8. `W()` - 27 edges
9. `5. Process and package layout` - 26 edges
10. `Pattern` - 25 edges

## Surprising Connections (you probably didn't know these)
- `runCheck()` --calls--> `RunWithOptions()`  [INFERRED]
  cmd/janusfs/check.go → internal/check/check.go
- `runDaemon()` --calls--> `ApplyEnv()`  [INFERRED]
  cmd/janusfs/daemon.go → internal/config/config.go
- `runDaemon()` --calls--> `ApplyFile()`  [INFERRED]
  cmd/janusfs/daemon.go → internal/config/config.go
- `runDaemon()` --calls--> `Default()`  [INFERRED]
  cmd/janusfs/daemon.go → internal/config/config.go
- `runDaemon()` --calls--> `SocketPath()`  [INFERRED]
  cmd/janusfs/daemon.go → internal/control/control.go

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

## Communities (121 total, 33 thin omitted)

### Community 0 - "FUSE File Operations"
Cohesion: 0.06
Nodes (58): Root, InodeEmbedder, Errno, T, TestToErrnoMapsEachSentinel(), TestToErrnoMatchesWrappedSentinels(), ToErrno(), Open() (+50 more)

### Community 1 - "Architecture & Design Decisions"
Cohesion: 0.05
Nodes (81): Agent session, 9. Backing access layer, Cache isolation: direct I/O + zero timeouts for real files (FR-34), cmd/janusfs (entrypoint, DI, cobra), Concurrent re-redaction: serve previous bytes or block (FR-23), 16. Configuration, logging, and process wiring, Crash recovery: detached supervisor polls then force-unmounts (FR-35), Decision (ALLOWED | MASKED | HIDDEN) (+73 more)

### Community 2 - "Init & Daemon Commands"
Cohesion: 0.06
Nodes (68): Command, newInitCmd(), runInit(), runInitGlobal(), T, TestRunInit_ForceOverwrites(), TestRunInit_RefusesToOverwriteWithoutForce(), TestRunInit_WritesPolicyTemplate() (+60 more)

### Community 3 - "Rule Matching Engine"
Cohesion: 0.08
Nodes (58): caseInsensitiveVolume(), flipCase(), compilePatternFold(), Regexp, gitCheckIgnore(), T, runGit(), TestGitConformance() (+50 more)

### Community 4 - "Pattern Library"
Cohesion: 0.09
Nodes (56): Buffer, Builtins(), containsIgnoreCase(), getBuiltinPreFilter(), Regexp, init(), IsReserved(), LookupBuiltin() (+48 more)

### Community 5 - "Diagnostics & Check Reporting"
Cohesion: 0.11
Nodes (49): Finding, levelInfo, Match, matcher, Options, Report, Severity, treeEntry (+41 more)

### Community 6 - "CLI Check Command"
Cohesion: 0.09
Nodes (45): Command, Report, newCheckCmd(), printCheckMatches(), printCheckReport(), runCheck(), appendPolicyFixture(), captureStdout() (+37 more)

### Community 7 - "CodeMirror Vendor JS"
Cohesion: 0.16
Nodes (53): Le(), Me(), I(), a(), ae(), b(), be(), C() (+45 more)

### Community 8 - "Implementation Plans (PRPs)"
Cohesion: 0.06
Nodes (52): PRP 01 Correctness fixes, PRP 02 Crash recovery watchdog, PRP 03 Decision cache, PRP 04 Linux namespace exec, PRP 05 Dirfd backing layer, PRP 06 Process identity, PRP 07 macOS path-preserving, PRP 08 Reload revocation (+44 more)

### Community 9 - "Mount Layer & Provider"
Cohesion: 0.11
Nodes (32): Element, T, TestVirtualDirUnit(), copyAt(), Context, Mutex, Uint64, NewContentKey() (+24 more)

### Community 10 - "PID & Process Management"
Cohesion: 0.11
Nodes (36): pidfilePath(), pruneMirrorDirs(), readPidfile(), readPidfileMountpoint(), removePidfile(), T, TestPidfilePath_DiffersForDifferentMountpoints(), TestPidfilePath_StableForSameMountpoint() (+28 more)

### Community 11 - "Policy Engine"
Cohesion: 0.12
Nodes (32): decisionKey, dirConfigFP, Engine, Resolution, Int64, buildConfigSnapshot(), Decision, RuleSet (+24 more)

### Community 12 - "Process Identity & Agent Detection"
Cohesion: 0.11
Nodes (24): classify(), isAgent(), T, scrubEnv(), startChild(), TestCacheHitCounter(), TestEnvironSelfReturnsSomething(), TestIsAgentAncestryWalk() (+16 more)

### Community 13 - "CodeMirror Core"
Cohesion: 0.09
Nodes (26): ao(), At(), Bo(), br(), ct(), dt(), el(), fe() (+18 more)

### Community 14 - "HTTP API Server"
Cohesion: 0.15
Nodes (15): Server, VFSStats, Request, FS, Handler, HandlerFunc, Context, ResponseWriter (+7 more)

### Community 15 - "CodeMirror Editor"
Cohesion: 0.13
Nodes (31): A(), al(), cl(), D(), de(), dl(), $e(), fl() (+23 more)

### Community 16 - "History Store (SQLite)"
Cohesion: 0.13
Nodes (20): DB, OpRollup, opRow, Store, Context, Duration, Mutex, Time (+12 more)

### Community 17 - "Observability & Metrics"
Cohesion: 0.10
Nodes (18): Counter, CounterVec, Gauge, HistogramVec, formatBytes(), Time, Decision, knownDecisions() (+10 more)

### Community 18 - "Daemon Tests"
Cohesion: 0.16
Nodes (26): fakeRuntime(), T, TestBrowserOpenCommandByPlatform(), TestChildMountsUnder(), TestDaemonCall_NoDaemon(), TestDaemonIndex_FallsBackToSrcWithoutLabel(), TestDaemonIndex_NotFoundForOtherPaths(), TestDaemonIndex_RendersLabelAndEscapes() (+18 more)

### Community 19 - "CodeMirror Modes"
Cohesion: 0.15
Nodes (27): an(), ce(), ci(), Cn(), i(), ie(), J(), jr() (+19 more)

### Community 20 - "Virtual FUSE Node"
Cohesion: 0.15
Nodes (16): AttrOut, Context, DirStream, EntryOut, Errno, FileHandle, Inode, ReadResult (+8 more)

### Community 21 - "Vendor JS Utilities"
Cohesion: 0.15
Nodes (25): bi(), Di(), Ei(), eo(), F(), Fi(), gi(), io() (+17 more)

### Community 22 - "Documentation & Knowledge"
Cohesion: 0.12
Nodes (24): Architecture, Code Conventions, Packages, Working with SPEC.md, Quick Reference Commands, Formatting and Linting, Assets, Leak Channels (+16 more)

### Community 23 - "Exec Runner & Path Rewriter"
Cohesion: 0.13
Nodes (16): Listener, isNameChar(), isPathChar(), ReplacePaths(), T, TestReplacePaths(), callDaemon(), findSourceAndMount() (+8 more)

### Community 24 - "Control Protocol & Daemon IPC"
Cohesion: 0.17
Nodes (9): Conn, daemonRequest, daemonResponse, daemon, Listener, Logger, mountStatus, Mutex (+1 more)

### Community 25 - "Community 25"
Cohesion: 0.22
Nodes (18): T, TestCreateGating(), TestLinkDeniesLaunderingMaskedFile(), TestListxattrGating(), TestReloadTakesEffectWithoutRemount(), TestVirtualDir(), appendPolicyFixture(), T (+10 more)

### Community 26 - "Community 26"
Cohesion: 0.16
Nodes (22): ai(), dn(), en(), fn(), H(), hi(), ii(), M() (+14 more)

### Community 27 - "Community 27"
Cohesion: 0.20
Nodes (19): Command, Context, Duration, Logger, newWatchdogCmd(), runWatchdog(), spawnWatchdog(), stopWatchdog() (+11 more)

### Community 28 - "Community 28"
Cohesion: 0.16
Nodes (15): MacFUSEStatus, MountInfo, Report, RuntimeInfo, WatchdogStatus, checkMacFUSE(), checkMacFUSE(), Run() (+7 more)

### Community 29 - "Community 29"
Cohesion: 0.15
Nodes (21): ae(), Bn(), bt(), er(), go(), he(), ir(), jo() (+13 more)

### Community 30 - "Community 30"
Cohesion: 0.15
Nodes (21): Daemon watchdog subcommand, Hardlink escape prevention, Process identity (Tier 2), Order and gating sequence, Platform isolation model, Product Requirement Prompt (PRP), PRP-01 Correctness fixes, PRP-02 Crash recovery watchdog (+13 more)

### Community 31 - "Community 31"
Cohesion: 0.13
Nodes (20): api.Server (per-mount dashboard handler), internal/apperrors (errno mapping), janusfs CLI clients (mount|umount|update|path), ContentKey (path,mtime,size,inode,gen), ~/.janusfs/daemon.sock control socket, Dashboard HTTP (127.0.0.1:7381), Decision (HIDDEN > MASKED > ALLOWED), engine.Engine (atomic rule snapshot) (+12 more)

### Community 32 - "Community 32"
Cohesion: 0.33
Nodes (19): T, TestAuthMissing(), TestAuthQueryParam(), TestConfigSaveTriggersReload(), TestHeaders(), TestHistoryEndpointNoStore(), TestHostSecurity(), TestLatencyEndpointRemoved() (+11 more)

### Community 33 - "Community 33"
Cohesion: 0.16
Nodes (15): Command, newExecCmd(), Command, newExplainCmd(), Command, main(), newRootCmd(), callDaemon() (+7 more)

### Community 34 - "Community 34"
Cohesion: 0.20
Nodes (16): classifyMountRecords(), Command, mountStatus, Writer, newMountsCmd(), printMountListings(), runMounts(), T (+8 more)

### Community 35 - "Community 35"
Cohesion: 0.11
Nodes (18): CGO_ENABLED=0 policy, Conventional Commits changelog, SHA256 checksums, GitHub Releases, Homebrew tap publication, NFR-7 Single static binary, GoReleaser release pipeline, CycloneDX SBOM via syft (+10 more)

### Community 36 - "Community 36"
Cohesion: 0.16
Nodes (10): C(), E(), F(), h(), L(), m(), N(), s() (+2 more)

### Community 37 - "Community 37"
Cohesion: 0.22
Nodes (13): b(), c(), g(), h(), k(), m(), N(), o() (+5 more)

### Community 38 - "Community 38"
Cohesion: 0.17
Nodes (10): CancelFunc, Context, Logger, makeObserver(), startMount(), Context, Logger, mountRuntime (+2 more)

### Community 39 - "Community 39"
Cohesion: 0.33
Nodes (14): a(), b(), C(), d(), g(), h(), m(), n() (+6 more)

### Community 40 - "Community 40"
Cohesion: 0.23
Nodes (11): leadingDigits(), nullTerminatedString(), parseKernelVersion(), T, TestNullTerminatedString(), TestParseKernelVersion(), checkDevFuse(), checkKernelVersion() (+3 more)

### Community 41 - "Community 41"
Cohesion: 0.22
Nodes (14): ar(), cr(), dr(), fr(), gn(), gr(), Hn(), hr() (+6 more)

### Community 42 - "Community 42"
Cohesion: 0.26
Nodes (10): parent(), parentAndStartTime(), parseParentAndStartTime(), parseStatFields(), readStatFields(), startTime(), T, TestParseParentAndStartTimeHandlesCommWithSpacesAndParens() (+2 more)

### Community 43 - "Community 43"
Cohesion: 0.32
Nodes (11): a(), B(), C(), e(), L(), M(), o(), q() (+3 more)

### Community 44 - "Community 44"
Cohesion: 0.27
Nodes (10): daemonLogPath(), Command, newLogsCmd(), startDaemonBackground(), MountStatus, Response, Call(), Conn (+2 more)

### Community 45 - "Community 45"
Cohesion: 0.27
Nodes (9): Logger, Writer, New(), SetOutput(), T, TestNewConcurrentWithSetOutputIsSafe(), TestNewProducesValidJSONWithComponent(), TestSetOutputLevelRespected() (+1 more)

### Community 46 - "Community 46"
Cohesion: 0.38
Nodes (11): assertMetric(), T, labelsMatch(), metricValue(), newBlockingSink(), TestRecorderDoesNotUsePathLabels(), TestRecorderEmitsPrometheusMetrics(), TestRecorderHistoryFanoutDropsInsteadOfBlocking() (+3 more)

### Community 47 - "Community 47"
Cohesion: 0.23
Nodes (12): b(), co(), je(), jn(), kl(), Ll(), ol(), Sl() (+4 more)

### Community 48 - "Community 48"
Cohesion: 0.20
Nodes (4): C(), q(), x(), z()

### Community 49 - "Community 49"
Cohesion: 0.25
Nodes (9): browserOpenCommand(), Command, Context, newDaemonCmd(), runDaemon(), Level, Logger, logLevel() (+1 more)

### Community 50 - "Community 50"
Cohesion: 0.27
Nodes (6): Context, Errno, ReadResult, Time, LoopbackFile, revocableHandle

### Community 51 - "Community 51"
Cohesion: 0.24
Nodes (10): Phase 0 Baseline — NFR-3 performance budgets, internal/obs Recorder with Prometheus native collectors, NFR-3 performance budget thresholds, Prometheus /metrics as single metrics surface, Regexp pre-filtering and allocation bypass, slices.SortFunc zero-allocation sorting, Prometheus-only Observability Implementation Plan, Prometheus-only Observability Design (+2 more)

### Community 52 - "Community 52"
Cohesion: 0.49
Nodes (9): a(), c(), d(), h(), i(), L(), m(), o() (+1 more)

### Community 53 - "Community 53"
Cohesion: 0.67
Nodes (8): buildJanusfsBinary(), T, readMountinfo(), skipIfUnsupportedPrivateMount(), TestNamespaceIsolation_ExitCodeAndSignals(), TestNamespaceIsolation_HostMountTableUnaffected(), TestNamespaceIsolation_NoDaemonRequired(), TestNamespaceIsolation_TeardownRestoresNormalAccess()

### Community 54 - "Community 54"
Cohesion: 0.46
Nodes (8): Agent (untrusted), ALLOWED → passthrough, HIDDEN → deny (EACCES), JanusFS Architecture SVG, JanusFS (FUSE mount), MASKED → redaction (RAM), Policy snapshot (.janusignore / .janusmask), Underlying real files on disk (trusted)

### Community 55 - "Community 55"
Cohesion: 0.54
Nodes (6): b(), g(), h(), i(), w(), y()

### Community 57 - "Community 57"
Cohesion: 0.52
Nodes (7): JanusFS Filesystem Boundary Illustration, Filesystem Boundary, Janus (Two-Faced God of Doorways), Policy Enforcement at the Filesystem Boundary, Three Faces of Access (Allowed, Masked, Hidden), Trusted Source Code Side, Untrusted AI Agent Side

### Community 58 - "Community 58"
Cohesion: 0.29
Nodes (7): mountRuntime, Dashboard multiplexing, ~/.janusfs directory, Observability event path, History SQLite store, makeObserver, Non-blocking observability

### Community 59 - "Community 59"
Cohesion: 0.52
Nodes (5): e(), f(), i(), o(), y()

### Community 62 - "Community 62"
Cohesion: 0.50
Nodes (4): Command, Report, newDoctorCmd(), printDoctorReport()

### Community 63 - "Community 63"
Cohesion: 0.60
Nodes (4): T, TestRequestResumeNeverSerializes(), TestRequestRoundTrip(), TestWriteResponseDefaultsOKFromError()

### Community 64 - "Community 64"
Cohesion: 0.80
Nodes (4): BenchmarkHostRead_NoActiveExecSession(), BenchmarkHostRead_WithActiveExecSession(), benchReadFile(), B

### Community 65 - "Community 65"
Cohesion: 0.60
Nodes (4): BenchmarkAncestryWalk(), BenchmarkIsAgentCacheHit(), BenchmarkStartTime(), B

### Community 66 - "Community 66"
Cohesion: 0.80
Nodes (4): e(), f(), i(), l()

### Community 67 - "Community 67"
Cohesion: 0.50
Nodes (5): env-value redaction pattern, Hide rules, Mask rules with patterns, JanusFS policy version 1, Secret pattern library

### Community 68 - "Community 68"
Cohesion: 0.83
Nodes (4): pruneStaleRegistry(), LoadMounts(), RemoveMount(), TestMountsRegistry_RoundTrip()

### Community 70 - "Community 70"
Cohesion: 0.50
Nodes (4): janusfs check --matches policy preview, Mount registry self-healing on daemon resume, Mounts Cleanup and Check Matches Implementation Plan, Mount Registry Cleanup and Check Matches Design

### Community 71 - "Community 71"
Cohesion: 0.50
Nodes (4): Diagnostic Chain, Go Toolchain Hang Investigation, Impact on JanusFS Work, Build Requirements

### Community 72 - "Community 72"
Cohesion: 0.67
Nodes (3): discoverSourceRoot(), Context, Run()

### Community 73 - "Community 73"
Cohesion: 0.83
Nodes (3): parent(), parentAndStartTime(), startTime()

### Community 76 - "Community 76"
Cohesion: 0.67
Nodes (3): GoReleaser build and release, .github/workflows/ci.yml — CI pipeline, .github/workflows/release.yml — Release pipeline

### Community 77 - "Community 77"
Cohesion: 0.67
Nodes (3): Leak oracle, Length-preserving redaction, Redaction streaming modes

### Community 78 - "Community 78"
Cohesion: 0.67
Nodes (3): gate() decision-to-errno, Operation matrix, xattr redaction side channel

### Community 79 - "Community 79"
Cohesion: 0.67
Nodes (3): Dead code audit methodology, Scaffolding traps vs genuine dead code, API standalone-serve dead code cluster

## Ambiguous Edges - Review These
- `JanusFS Filesystem Boundary Illustration` → `Policy Enforcement at the Filesystem Boundary`  [AMBIGUOUS]
  docs/janus_art.png · relation: references
- `JanusFS Filesystem Boundary Illustration` → `Three Faces of Access (Allowed, Masked, Hidden)`  [AMBIGUOUS]
  docs/janus_art.png · relation: references

## Knowledge Gaps
- **90 isolated node(s):** `run_spike.sh script`, `unmountAttempt`, `github.com/sarathsp06/janusfs`, `fileSettings`, `decisionKey` (+85 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **33 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **What is the exact relationship between `JanusFS Filesystem Boundary Illustration` and `Policy Enforcement at the Filesystem Boundary`?**
  _Edge tagged AMBIGUOUS (relation: references) - confidence is low._
- **What is the exact relationship between `JanusFS Filesystem Boundary Illustration` and `Three Faces of Access (Allowed, Masked, Hidden)`?**
  _Edge tagged AMBIGUOUS (relation: references) - confidence is low._
- **Why does `mountRuntime` connect `Community 38` to `Mount Layer & Provider`, `Policy Engine`, `HTTP API Server`, `History Store (SQLite)`, `Observability & Metrics`, `Daemon Tests`, `Control Protocol & Daemon IPC`?**
  _High betweenness centrality (0.071) - this node is a cross-community bridge._
- **Why does `Engine` connect `Policy Engine` to `FUSE File Operations`, `Diagnostics & Check Reporting`, `Community 38`?**
  _High betweenness centrality (0.053) - this node is a cross-community bridge._
- **Why does `Server` connect `HTTP API Server` to `Community 32`, `Community 38`, `Community 46`, `History Store (SQLite)`, `Exec Runner & Path Rewriter`, `Control Protocol & Daemon IPC`?**
  _High betweenness centrality (0.049) - this node is a cross-community bridge._
- **Are the 3 inferred relationships involving `P()` (e.g. with `al()` and `il()`) actually correct?**
  _`P()` has 3 INFERRED edges - model-reasoned connections that need verification._
- **Are the 2 inferred relationships involving `s()` (e.g. with `E()` and `T()`) actually correct?**
  _`s()` has 2 INFERRED edges - model-reasoned connections that need verification._