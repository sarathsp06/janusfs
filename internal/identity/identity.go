package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
)

// Identity represents the multi-attribute process identity tuple (PRP §3.1).
type Identity struct {
	PID           int    `json:"pid"`
	StartTime     int64  `json:"start_time"`
	PPIDChainHash string `json:"ppid_chain_hash"`
	BootUUID      string `json:"boot_uuid"`
}

// Registry tracks verified agent processes and validates caller identities (PRP §3.1).
type Registry struct {
	mu     sync.RWMutex
	agents map[int]Identity
}

// NewRegistry constructs a new thread-safe process identity registry.
func NewRegistry() *Registry {
	return &Registry{
		agents: make(map[int]Identity),
	}
}

// Register adds or updates an agent process identity tuple.
func (r *Registry) Register(pid int, id Identity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[pid] = id
}

// Unregister removes an agent process from the registry.
func (r *Registry) Unregister(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, pid)
}

// IsEmpty reports whether there are no registered agent processes.
// Used for backward compatibility: if the registry is empty, we fall back to filtering everyone.
func (r *Registry) IsEmpty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents) == 0
}

// Verify checks if the calling PID matches a registered agent process or any of its verified descendants.
// It implements PID reuse prevention, parent ancestry tree tracking, and boot session validation.
func (r *Registry) Verify(pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.agents) == 0 {
		return true // Fallback/legacy mode: if no agent is registered, apply filters to everyone.
	}

	curr := pid
	visited := map[int]bool{}

	// Walk up the parent process tree of the caller (PRP §3.1)
	for curr > 0 && curr != 1 && !visited[curr] {
		visited[curr] = true

		if id, ok := r.agents[curr]; ok {
			// 1. PID reuse check (Start Time verification)
			_, currStart, err := getOSProcessInfo(curr)
			if err != nil || currStart != id.StartTime {
				// Mismatch or error triggers immediate registry purging (PRP §3.1)
				delete(r.agents, curr)
				return false
			}

			// 2. Ancestry hash & Boot UUID verification for the registered agent itself
			if curr == pid {
				currHash, err := GetPPIDChain(curr)
				if err != nil || currHash != id.PPIDChainHash {
					return false
				}
				boot, err := getBootUUID()
				if err != nil || boot != id.BootUUID {
					return false
				}
			}

			return true
		}

		ppid, _, err := getOSProcessInfo(curr)
		if err != nil {
			break
		}
		curr = ppid
	}

	return false
}

// GetPPIDChain walks the parent process tree up to PID 1 (or 0), concatenates them, and computes the SHA-256 hash.
func GetPPIDChain(pid int) (string, error) {
	ppid, _, err := getOSProcessInfo(pid)
	if err != nil {
		return "", err
	}
	var chain []string
	curr := ppid
	visited := map[int]bool{pid: true}
	for curr > 0 && curr != 1 && !visited[curr] {
		visited[curr] = true
		chain = append(chain, strconv.Itoa(curr))
		nextPpid, _, err := getOSProcessInfo(curr)
		if err != nil {
			break
		}
		curr = nextPpid
	}
	if curr == 1 {
		chain = append(chain, "1")
	}
	chainStr := strings.Join(chain, "->")
	h := sha256.Sum256([]byte(chainStr))
	return hex.EncodeToString(h[:]), nil
}

// GetBootUUID retrieves the current OS boot session identifier.
func GetBootUUID() (string, error) {
	return getBootUUID()
}

// GetProcessStartTime retrieves the birth start time of a process.
func GetProcessStartTime(pid int) (int64, error) {
	_, startTime, err := getOSProcessInfo(pid)
	return startTime, err
}
