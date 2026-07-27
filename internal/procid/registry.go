package procid

import (
	"strings"
	"sync"
	"sync/atomic"
)

// sessionEnvVar is the environment variable a registered agent process
// carries. It is inherent to that process's descendants — inherited across
// fork, preserved across setsid and reparenting to PID 1 — which is what
// makes environ the PRIMARY IsAgent resolution mechanism (see isAgent()).
const sessionEnvVar = "JANUSFS_SESSION"

// Registry answers whether a caller belongs to a registered agent session.
// Implementations must be safe for concurrent use from FUSE handlers.
type Registry interface {
	Register(sessionToken string, root Identity)
	Unregister(sessionToken string)
	IsAgent(pid int) bool
	Stats() Stats
}

// Stats is a point-in-time snapshot for janusfs doctor / the dashboard.
type Stats struct {
	Sessions  int
	Lookups   uint64
	CacheHits uint64
	Agent     uint64
	Host      uint64
}

// MemRegistry is the in-memory Registry. It cannot outlive a reboot, which
// is exactly why no boot UUID is needed to key it — every entry is fresh
// per-boot by construction.
//
// Concurrency: sessions and cache are guarded by their own mutexes so a
// hot IsAgent path can revalidate without contending with Register/
// Unregister. The atomics are for cheap lock-free counter reads in Stats.
type MemRegistry struct {
	mu       sync.RWMutex
	sessions map[string]Identity // sessionToken -> root

	cacheMu sync.Mutex
	cache   map[int]cacheEntry // pid -> (startTime, verdict)

	lookups, cacheHits, agent, host atomic.Uint64
}

type cacheEntry struct {
	startTime int64
	isAgent   bool
}

func NewMemRegistry() *MemRegistry {
	return &MemRegistry{
		sessions: make(map[string]Identity),
		cache:    make(map[int]cacheEntry),
	}
}

func (r *MemRegistry) Register(sessionToken string, root Identity) {
	r.mu.Lock()
	r.sessions[sessionToken] = root
	r.mu.Unlock()
}

func (r *MemRegistry) Unregister(sessionToken string) {
	r.mu.Lock()
	delete(r.sessions, sessionToken)
	r.mu.Unlock()
	// Drop the whole per-PID cache: a session ending flips descendants'
	// verdicts from agent to host, and the cheapest correct move is to
	// re-derive from scratch on next lookup rather than track which cache
	// entries transitively depended on which session.
	r.cacheMu.Lock()
	r.cache = make(map[int]cacheEntry)
	r.cacheMu.Unlock()
}

// sessionRoots returns a snapshot of registered root identities keyed by
// token — used by IsAgent's environ and ancestry-walk paths.
func (r *MemRegistry) sessionRoots() map[string]Identity {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Identity, len(r.sessions))
	for k, v := range r.sessions {
		out[k] = v
	}
	return out
}

// hasToken reports whether token names a currently-registered session.
func (r *MemRegistry) hasToken(token string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.sessions[token]
	return ok
}

// isRegisteredRoot reports whether (pid, startTime) matches any registered
// session's root exactly.
func (r *MemRegistry) isRegisteredRoot(pid int, stime int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range r.sessions {
		if id.PID == pid && id.StartTime == stime {
			return true
		}
	}
	return false
}

func (r *MemRegistry) IsAgent(pid int) bool {
	r.lookups.Add(1)
	verdict := isAgent(r, pid)
	if verdict {
		r.agent.Add(1)
	} else {
		r.host.Add(1)
	}
	return verdict
}

func (r *MemRegistry) Stats() Stats {
	r.mu.RLock()
	n := len(r.sessions)
	r.mu.RUnlock()
	return Stats{
		Sessions:  n,
		Lookups:   r.lookups.Load(),
		CacheHits: r.cacheHits.Load(),
		Agent:     r.agent.Load(),
		Host:      r.host.Load(),
	}
}

// tokenFromEnviron scans an environment block for JANUSFS_SESSION=<value>
// and returns the value if present.
func tokenFromEnviron(env []string) (string, bool) {
	pfx := sessionEnvVar + "="
	for _, e := range env {
		if strings.HasPrefix(e, pfx) {
			return e[len(pfx):], true
		}
	}
	return "", false
}
