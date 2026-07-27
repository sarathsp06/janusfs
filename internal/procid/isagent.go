package procid

// maxAncestryDepth caps the parent() walk in step 3 of isAgent. In real
// process trees agent subprocesses sit a few levels under their session
// root; a generous cap here keeps a runaway walk (a bug elsewhere reporting
// nonsensical ppids) from turning into an unbounded per-op cost.
const maxAncestryDepth = 32

// isAgent implements the resolution order documented in
// PRPs/06-process-identity.md. Four steps, each falling through to the next
// on a "no verdict here" outcome; the FIRST that produces a definitive
// answer wins.
//
//  1. Cache hit for (pid, startTime)?
//     Re-read startTime and compare against the cached pair.
//     - match    → return the cached verdict.
//     - mismatch → the PID was recycled; drop the entry and continue.
//
//  2. environ(pid) contains JANUSFS_SESSION=<token> for a currently-
//     registered session?
//     → agent. PRIMARY because it survives fork, setsid, and reparenting.
//
//  3. Walk parent(pid) up to a registered session root or PID 1.
//     → agent if a registered root is found.
//
//  4. Anything else, including any error at any step
//     → NOT an agent. This is the deliberately-inverted fail direction:
//     an unidentifiable caller is a host process and gets passthrough.
//     Denying it would break the user's editor and shell for zero security
//     benefit, since the process being denied was never the agent.
//     See process-identity.md and SPEC.md NFR-2 for the reasoning.
//
// Memoization is keyed on (pid, startTime), which needs no TTL for
// correctness: a recycled PID has a different start time, so it cannot
// collide with a cached entry.
func isAgent(r *MemRegistry, pid int) bool {
	cur, err := startTime(pid)
	if err != nil {
		return false
	}

	// Step 1 — cache lookup with start-time revalidation.
	r.cacheMu.Lock()
	if e, ok := r.cache[pid]; ok {
		if e.startTime == cur {
			r.cacheMu.Unlock()
			r.cacheHits.Add(1)
			return e.isAgent
		}
		// PID was recycled — drop the stale entry, fall through.
		delete(r.cache, pid)
	}
	r.cacheMu.Unlock()

	verdict := classify(r, pid, cur)

	r.cacheMu.Lock()
	r.cache[pid] = cacheEntry{startTime: cur, isAgent: verdict}
	r.cacheMu.Unlock()
	return verdict
}

// classify runs resolution steps 2 and 3. Split out so step 1's cache
// bookkeeping stays readable.
func classify(r *MemRegistry, pid int, stime int64) bool {
	// Step 2 — environ.
	if env, err := environ(pid); err == nil {
		if tok, ok := tokenFromEnviron(env); ok {
			if r.hasToken(tok) {
				return true
			}
		}
	}

	// Step 3 — ancestry walk. The starting pid itself may be a registered
	// root (a rare case, but a session's root process is itself an agent
	// as far as the mount is concerned), so check it before ascending.
	if r.isRegisteredRoot(pid, stime) {
		return true
	}
	cur := pid
	for depth := 0; depth < maxAncestryDepth; depth++ {
		p, err := parent(cur)
		if err != nil || p <= 1 || p == cur {
			return false
		}
		pst, err := startTime(p)
		if err != nil {
			return false
		}
		if r.isRegisteredRoot(p, pst) {
			return true
		}
		cur = p
	}
	return false
}
