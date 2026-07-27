## 2026-07-27 - Remove Dead Mock Daemon Code

**Learning:**
We discovered that the main daemon binary contained a developer-only `JANUSFS_MOCK_DEV` environment flag that bypassed standard resume flows to start two mock mounts. This block was coupled with `cmd/janusfs/mock_dev.go`, which spun up artificial mounts utilizing hardcoded home directory paths (`/home/jules/...`) belonging to a different development machine. The mock path logic was obsolete, dead in production builds, circumvented normal mounting validation/resume protocols, and was identified as a known gap. Deleting this logic removes unnecessary files and complexity from the daemon start path.

**Action:**
We removed `cmd/janusfs/mock_dev.go` and completely cleaned up the conditional mock-mount logic within `cmd/janusfs/daemon.go`. We also removed the corresponding entry from the known-gaps register (`docs/knowledge/known-gaps.md`) and documented this high-value simplification in the knowledge log.
