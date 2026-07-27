---
okf_version: "0.2"
---

# Start here

* [Architecture](architecture.md) - process topology, the request path, and how the daemon wires a mount together.
* [Known gaps](known-gaps.md) - the defect and risk register: what is broken or unproven today, ranked.
* [Conventions](conventions.md) - the house rules a change must follow, and how to build and test.

# The read path (how a byte gets decided)

* [Policy engine](policy-engine.md) - `.janusignore`/`.janusmask` discovery, compilation, and precedence into one Decision.
* [Masking pipeline](masking-pipeline.md) - length-preserving redaction, the RAM cache, and its validity key.
* [FUSE adapter](fuse-adapter.md) - the as-built operation matrix over go-fuse, and the synthetic `.janusfs` directory.

# The control plane

* [CLI and daemon](cli-and-daemon.md) - command tree, the control socket protocol, mount lifecycle, and crash recovery.
* [Config and on-disk layout](config-and-layout.md) - the one `Config` struct, precedence, and everything under `~/.janusfs`.
* [Observability](observability.md) - event bus, metrics, SQLite rollups, and the dashboard.

# Isolation (the current design frontier)

* [Exec and path parity](exec-and-path-parity.md) - why `janusfs exec` rewrites path strings, and why that is the central limitation.
* [Platform isolation models](platform-isolation.md) - Linux mount namespaces versus a macOS path-preserving overmount, and what each can enforce.
* [Process identity](process-identity.md) - identifying the calling process, and what that can and cannot buy.

# Also in this repo

* [PRPs/](/PRPs/README.md) - implementation blueprints for queued work, in execution order, with a requirement-coverage matrix.
* [SPEC.md](/SPEC.md) - the binding requirements contract.
* [AGENTS.md](/AGENTS.md) - orientation for a coding agent.
* [docs/THREAT_MODEL.md](/docs/THREAT_MODEL.md) - the living threat model.
