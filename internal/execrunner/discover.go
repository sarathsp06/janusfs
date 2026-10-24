package execrunner

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sarathsp06/janusfs/internal/rules"
)

// Plan is everything `janusfs exec` decides about a run before any namespace
// exists: which tree is protected and what that tree's own policy asks for.
// It is computed once, on every platform, so the CLI can show it (the start
// banner, --dry-run) and refuse early with a cause, before the Linux-only
// enforcement machinery is involved at all.
type Plan struct {
	// Cwd is the directory `janusfs exec` was invoked from; the command runs
	// there, inside the boundary, since the same path is valid on both sides.
	Cwd string `json:"cwd"`
	// Src is the discovered source root: the nearest ancestor-or-self of Cwd
	// that carries a .janusfs.yml. This is the directory the filtered view
	// replaces.
	Src string `json:"src"`
	// PolicyPath is Src's .janusfs.yml, the root policy file.
	PolicyPath string `json:"policyPath"`
	// ExecPolicy is the root policy file's optional `exec:` section.
	ExecPolicy rules.ExecPolicy `json:"execPolicy"`
}

// Discover resolves the Plan for a `janusfs exec` run from cwd. It refuses,
// with a cause and a remedy, when no policy file is found at or above cwd —
// defaulting to cwd would provision an unpoliced view over whatever directory
// happens to be current (a user's entire home directory, in the worst case),
// which is the opposite of what this tool exists to prevent. It never talks
// to a daemon: exec must work with none running.
func Discover(cwd string) (Plan, error) {
	src, err := discoverSourceRoot(cwd)
	if err != nil {
		return Plan{}, err
	}
	pol, err := rules.ReadExecPolicy(src)
	if err != nil {
		// Fail closed: a policy that cannot be read is not a policy that can
		// be honoured, and silently running with the defaults instead would
		// drop a restriction the repository asked for.
		return Plan{}, fmt.Errorf("exec: %w", err)
	}
	return Plan{
		Cwd:        cwd,
		Src:        src,
		PolicyPath: filepath.Join(src, rules.PolicyFileName),
		ExecPolicy: pol,
	}, nil
}

// discoverSourceRoot walks up from cwd looking for a directory containing
// .janusfs.yml. See Discover for why it refuses rather than defaulting.
func discoverSourceRoot(cwd string) (string, error) {
	curr := cwd
	for {
		currAbs, err := filepath.Abs(curr)
		if err != nil {
			break
		}
		if _, err := os.Stat(filepath.Join(currAbs, rules.PolicyFileName)); err == nil {
			return currAbs, nil
		}
		parent := filepath.Dir(currAbs)
		if parent == currAbs {
			break
		}
		curr = parent
	}
	return "", fmt.Errorf("exec: no JanusFS policy found for %s — refusing to mount an unpoliced tree\nRemedy: run `janusfs init` here, or in the project root above this directory", cwd)
}
