# SPEC Amendments

Per SPEC.md §20 (Ground rules, item 1): if an implementer needs a behavior the spec doesn't define, they must not invent it silently. Add an entry here with rationale, implement the most conservative (fail-closed) interpretation, and flag it in the relevant report.

Format per entry:

```
## YYYY-MM-DD — <short title>

**Spec gap:** what SPEC.md doesn't say.
**Interpretation adopted:** what was implemented, and why it's the fail-closed / most conservative reading.
**Affected sections:** SPEC.md §... / FR-...
**Status:** proposed | accepted | superseded by <later entry>
```

---

## 2026-07-16 — Add `caarlos0/env` for env-var config loading

**Spec gap:** SPEC.md §15 says `internal/config` is populated "from CLI flags/env" and env vars are "a secondary override, not primary," but doesn't say how the env side is parsed. §20.4's dependency allowlist predates this need and doesn't list an env-parsing library.

**Interpretation adopted:** hand-rolling `os.Getenv` calls per field for every tunable (and keeping the two mechanisms — flags and env — in sync by hand as fields are added) is exactly the kind of manual, error-prone plumbing SPEC.md §21's leaf-package conventions try to avoid elsewhere (see the `internal/apperrors`/`internal/logging` single-translation-point pattern). Using a small, struct-tag-driven library keeps `internal/config` declarative: each `Config` field carries an `env:"JANUSFS_..."` tag and one `env.Parse` call fills in any set environment variables, leaving fields at their `Default()` values otherwise. CLI flags are parsed afterward and take precedence, matching §15's ordering.

Library chosen: **`github.com/caarlos0/env/v11`** — pure Go, no transitive dependencies, actively maintained, does exactly this one thing (struct-tag env parsing) and nothing else (no config-file parsing, no CLI parsing, no network calls), so it doesn't expand the dependency footprint SPEC.md §20.4 is trying to bound.

**§20.4 amendment:** add `github.com/caarlos0/env/v11` to the allowed-without-asking dependency list, restricted to `internal/config`.

**Affected sections:** SPEC.md §15, §20.4, §21 (internal/config's Load).
**Status:** accepted.

**Follow-up required:** this dependency could not be fetched via `go get`/`go mod tidy` in the session that added it — the local `go` toolchain was hanging at the dyld/code-signature-check stage before running at all (see conversation; likely a network security tool intercepting a `trustd`/OCSP check). `go.mod`'s `require` block has a manually added line for `github.com/caarlos0/env/v11`; **run `go mod tidy` once the toolchain is confirmed working** to populate `go.sum` and verify the version/checksum.

**Resolved 2026-07-17:** toolchain works again (a restart cleared the exec-authorization stall). `go mod tidy` ran cleanly and resolved `github.com/caarlos0/env/v11 v11.2.2` with real checksums.

---

## 2026-07-17 — Use `spf13/cobra` for the CLI instead of hand-rolled subcommand dispatch

**Spec gap:** SPEC.md §20.4 originally allowed only stdlib for CLI plumbing, reasoning (in an earlier design pass) that hand-rolled `flag.NewFlagSet`-per-subcommand dispatch was simple enough not to need a dependency. In practice this created a real bug: `internal/config.Load` parsed positional args and flags itself via stdlib `flag.FlagSet`, which stops scanning for flags at the first non-flag token — so `janusfs mount <src> <mountpoint> --ui-port=9000` would silently ignore `--ui-port`. Requiring a strict flags-before-positionals order works but is a footgun a user (or an agent following FR-1's example syntax literally) would likely trip on, and hand-rolling subcommand routing, `--help` generation, and error formatting (FR-30/FR-33) for five subcommands (`mount`, `umount`, `init`, `check`, `doctor`) duplicates what a mature CLI library already does correctly.

**Interpretation adopted:** use `github.com/spf13/cobra` (with its `spf13/pflag` dependency) for subcommand dispatch, flag registration, and help/usage text. This directly serves FR-30 ("one-line cause + one-line remedy... no raw Go errors reach the user") via `SilenceUsage`/`SilenceErrors` plus a custom error printer, and FR-33's "designed to be read" output expectation for `check`.

**Consequence for `internal/config`:** `Load(args []string)` (which owned `flag.FlagSet` construction) is removed. `internal/config` now only exposes `Default()`, `ApplyEnv(*Config) error` (wraps `env.Parse`), and `Validate() error`. `cmd/janusfs`'s cobra commands bind flags directly to `Config` fields via `cmd.Flags().IntVar(&cfg.UIPort, "ui-port", cfg.UIPort, ...)`, using the post-`ApplyEnv` value as each flag's default — preserving the same "flags win over env which wins over built-in defaults" ordering (SPEC §15), just with cobra/pflag doing the parsing instead of stdlib `flag`, and without the ordering footgun (pflag supports flags interspersed with positional arguments).

**Other library uses considered, per the same "use a library where it makes sense" principle, and their outcomes:**
- **TUI:** none added. SPEC's observability surface (§3.6, FR-25) is the embedded web dashboard, not a terminal UI; no SPEC requirement calls for one. If a TUI companion view is wanted later, `charmbracelet/bubbletea` is the natural pick — noted here rather than added speculatively.
- **Security-relevant needs** (the per-mount bearer token, §11): stdlib `crypto/rand` is the correct tool already (a CSPRNG is a CSPRNG; no library does this more correctly than stdlib), so nothing was added there.

**§20.4 amendment:** add `github.com/spf13/cobra` (and its `spf13/pflag`/`github.com/inconshreveable/mousetrap` transitive dependencies) to the allowed-without-asking list.

**Affected sections:** SPEC.md §15, §20.4, §21, §24 (cmd/janusfs's structure).
**Status:** accepted.

---

## 2026-07-17 — Global config directory for `.janusignore`/`.janusmask`, and gitignore library choice

**Spec gap:** FR-15/§2.4 define hierarchical discovery only from the mount root down to a path's directory — every rule lives inside `<src>`, git-committable next to the code it protects. There is no mechanism for rules a user wants applied to *every* mount on a machine (e.g. "always hide `*.pem` and mask AWS keys, regardless of which repo I point janusfs at"), and no designated on-disk location for such machine-wide defaults. SPEC.md §20.4/§24 also left the Phase 1 gitignore library choice open ("chosen at Phase 1 against a conformance suite").

**Interpretation adopted:**
- A new **global config directory**, `~/.janusfs/config/`, may contain a `.janusignore` and/or `.janusmask` file. These are consistent with the existing `~/.janusfs/run/` (pidfiles, FR-3) and `~/.janusfs/history/` (FR-42) conventions already in SPEC.md §3.8/cmd/janusfs's pidfile.go — one root (`~/.janusfs/`), one subdirectory per concern.
- Global rules are treated as a **virtual ancestor level above the mount root**: lowest precedence of all. Any rule inside `<src>` (at any depth) can override a global rule for a given path, matching git's "later/deeper wins" ordering (FR-12) — a global rule is the most "shallow" level there is. This is the fail-closed-safe choice for both file types: a global Hidden rule can only be *widened* into visibility by an explicit local negation (never silently), and a global Masked glob is only ever narrowed or overridden by more specific in-tree rules, never weakened by the mere existence of the global file being unknown to a repo's own reviewers.
- Global config files are **read-only through the mount** exactly like in-tree config files (FR-15) — same rationale (an agent must never be able to weaken its own sandbox), even though the global file physically lives outside `<src>`.
- `janusfs init --global` writes the same secure-default templates as `janusfs init [dir]` (FR-17) into `~/.janusfs/config/`, creating the directory (`0700`) if needed. `janusfs check` and `janusfs explain` (new, see below) always include the global level in their discovery, labeling findings/matches from it as `~/.janusfs/config/.janusignore` (etc.) rather than a path under the scanned tree, so a user reviewing output isn't confused about why a rule they don't see in the repo matched.
- **Gitignore matching: implemented from scratch, no third-party library.** SPEC.md §24 task 1 originally called for evaluating `go-git/gitignore` vs `sabhiram/go-gitignore` and wrapping the winner. Both were checked against the project's dependency-health bar (active development, real usage) before writing any code against either:

  | Library | Stars | Last push | Verdict |
  |---|---|---|---|
  | `sabhiram/go-gitignore` | 165 | 2024-02-16 (>2 years stale) | Rejected |
  | `go-git/gitignore` | (part of `go-git/go-git`, pulled in for one file-matching feature) | — | Rejected: drags in the entire go-git object-model dependency tree for a single gitignore matcher; janusfs's ignore semantics have nothing to do with an actual git repository (SPEC.md's own §2.1 rationale for "wrap, don't fork" was about semantics, not about accepting unrelated baggage) |

  `sabhiram/go-gitignore` was initially wired up (matching SPEC.md's literal text), but its per-file negation model doesn't compose across FR-12's hierarchical levels: it evaluates each `.janusignore` file's patterns as a closed set, so a bare `!pattern` line in a deeper file — with nothing to negate *within that same file* — is reported as "no match at all" (`MatchesPathHow` returns a nil pattern) rather than "explicitly re-included," making it impossible to let a deeper file's negation override a shallower file's exclusion (exactly what FR-12's "later match wins" requires). Combined with the staleness above, this was reason enough to drop it rather than work around the composition gap.

  **Adopted instead:** a from-scratch implementation (`internal/rules/glob.go`) — glob-to-RE2 translation covering `*`, `?`, `[...]` classes, and all four `**` forms (bare, leading `**/`, trailing `/**`, interior `/**/ `), plus `!`-negation and trailing-`/` directory-only markers, escaping (`\!`, `\#`). Precedence across hierarchical levels (including the new global level) is handled explicitly in `internal/rules/resolve.go`: patterns are evaluated level-by-level, shallowest first, and the *last matching* pattern across all applicable levels decides the outcome — this is a natural, correct generalization of "later wins" across files, not something the single-file-scoped library API could express. Verified against real `git check-ignore` as the oracle (SPEC.md §19's own listed risk mitigation) via `internal/rules/glob_test.go`'s `TestGitConformance`, which shells out to `git init`/`git check-ignore` for each pattern shape and asserts identical verdicts. This also means one less runtime dependency, and the matcher is exactly as testable as any other engine code (NFR-8) since it's ours.

**§20.4 amendment:** no gitignore library is added; §24 task 1 is satisfied by `internal/rules/glob.go` + its `git check-ignore` conformance suite instead of a wrapped dependency.

**Affected sections:** SPEC.md §2.4, FR-15, FR-17, §19 (risk mitigation, now implemented), §20.4, §24 (Phase 1 task 1 — reinterpreted as "write and conformance-test," not "wrap"), §28 (`janusfs check`); new: `janusfs explain` (not in SPEC.md; a new diagnostics subcommand, same spirit as `check`/`doctor` — surfaces the engine's per-path resolution and its matching rule for a single path, requested directly rather than only as a byproduct of a full-tree lint).
**Status:** accepted.

---

## 2026-07-17 — Dependency health check across the current and planned dependency list

**Spec gap:** SPEC.md §20.4 names specific libraries but doesn't establish a bar for *how* they're chosen beyond "documented," "maintained," "pure Go." Prompted by the go-gitignore staleness found above, every dependency actually in `go.mod` (plus the Phase 4 planned ones named in SPEC.md §7/§20.4) was checked against GitHub star count and last-push date as a cheap proxy for "actively maintained, not a supply-chain dead end."

| Dependency | Stars | Last push | Verdict |
|---|---|---|---|
| `jacobsa/fuse` | 572 | 2026-07-03 | Keep — SPEC.md's own choice, active, fallback plan already documented (SPEC.md §6) |
| `caarlos0/env/v11` | 6.2k | 2026-07-07 | Keep — active, single-purpose, no transitive deps |
| `spf13/cobra` | 44k | 2026-07-11 | Keep — de facto standard, very active |
| `spf13/pflag` | 2.8k | 2026-07-03 | Keep — cobra's own flag dependency, active |
| `inconshreveable/mousetrap` | 276 | 2022-11-29 | Keep — trivial (~40 lines), transitive-only via cobra on Windows, no realistic replacement warranted for something this small |
| `sabhiram/go-gitignore` | 165 | 2024-02-16 | **Removed** — see above |
| `modernc.org/sqlite` (Phase 4, not yet added) | n/a (hosted outside GitHub stars tracking; part of the actively-released `modernc.org`/`cznic` toolchain) | — | Still the right pick when Phase 4 lands: pure Go avoids cgo per SPEC.md's own rationale; re-verify activity at that time rather than now, since dependency health is a point-in-time check |
| `prometheus/client_golang` (Phase 4) | 6.0k | 2026-07-16 | Still fine when Phase 4 lands |
| `nhooyr.io/websocket` (Phase 4, SPEC.md's preferred pick) | now published as `github.com/coder/websocket` (5.3k stars, 2026-06-15) — same project, renamed | Re-verify the import path at Phase 4 time; `gorilla/websocket` (24.8k stars but last pushed 2025-03-19, maintenance-mode) remains the fallback SPEC.md already names |

**Interpretation adopted:** treat "low stars + no commits in over a year" as a standing red flag for any dependency proposed in this project, checked via `gh api repos/<owner>/<repo> --jq '{stars:.stargazers_count, pushed_at:.pushed_at, archived:.archived}'` (or the equivalent web view) before adding anything new, not just at the point a bug is found. Below that bar, the default is to write the needed logic ourselves when it's small and testable (as with the gitignore matcher, backed by the project's own linter/conformance tooling) rather than accept an unmaintained dependency for convenience.

**Affected sections:** SPEC.md §20.4 (adds a health-check step to "anything else requires a decision record").
**Status:** accepted.
