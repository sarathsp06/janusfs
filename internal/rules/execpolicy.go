package rules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ExecPolicy is the optional `exec:` section of a project root's .janusfs.yml:
// per-repository defaults for `janusfs exec`, so a repository can declare
// "agents working here run without network" once, instead of every operator
// remembering --net=none on every launch.
//
// It is read only from the discovered source root (the directory `janusfs
// exec` protects), never from subdirectory or global policy files: exec has
// exactly one boundary per run and that boundary is the root.
type ExecPolicy struct {
	// Net is the default network mode: "" (unset), "host", or "none". A
	// repository can only tighten what the machine-wide default allows:
	// "none" turns network isolation on by default, while "host" is a no-op
	// — it can never loosen a `none` coming from ~/.janusfs/settings.json or
	// JANUSFS_EXEC_NET. An explicit --net flag on the command line still
	// wins, because the operator at the keyboard is trusted.
	Net string `json:"net"`
}

// ErrInvalidExecNet reports an `exec.net` value other than host or none.
var ErrInvalidExecNet = errors.New("rules: exec.net must be \"host\" or \"none\"")

// ReadExecPolicy reads the `exec:` section of dir's .janusfs.yml. A missing
// file or a missing section yields the zero ExecPolicy; a present but invalid
// value is an error, so that a typo like `net: nnoe` is refused loudly by
// `janusfs exec` rather than silently treated as "host".
//
// Everything else in the file is ignored here — hide/allow/mask compile
// through Discover, and an unknown key never breaks either path.
func ReadExecPolicy(dir string) (ExecPolicy, error) {
	file := filepath.Join(dir, PolicyFileName)
	data, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ExecPolicy{}, nil
		}
		return ExecPolicy{}, fmt.Errorf("rules: reading %s: %w", file, err)
	}
	var pf struct {
		Exec struct {
			Net string `yaml:"net"`
		} `yaml:"exec"`
	}
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return ExecPolicy{}, fmt.Errorf("rules: parsing %s: %w", file, err)
	}
	switch pf.Exec.Net {
	case "", "host", "none":
		return ExecPolicy{Net: pf.Exec.Net}, nil
	default:
		return ExecPolicy{}, fmt.Errorf("%w (got %q in %s)", ErrInvalidExecNet, pf.Exec.Net, file)
	}
}
