// Package procid gives internal/mount a cheap way to answer "does this
// caller belong to a registered agent session?" per FUSE operation. Only
// the pieces PRP 06 Task 1 needs to run the gating benchmark are here so
// far: startTime(pid) and parent(pid). Everything else (Registry, IsAgent
// resolution order, session token plumbing) lands in a follow-up commit,
// after the benchmark confirms the cache-hit path fits inside NFR-3's
// budget.
package procid

// Identity is one process, unique for the lifetime of a boot. StartTime is
// what makes the pair unique: a recycled PID has a different start time, so
// a (PID, StartTime) pair can never be confused with an earlier process.
type Identity struct {
	PID       int
	StartTime int64
}
