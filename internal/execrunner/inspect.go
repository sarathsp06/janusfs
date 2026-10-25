package execrunner

import (
	"github.com/sarathsp06/janusfs/internal/check"
	"github.com/sarathsp06/janusfs/internal/nsexec"
)

// Enforcement says whether this host can actually confine a process tree,
// and if not, why — in the words `janusfs doctor` would use.
type Enforcement struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// Inspection is the full `janusfs exec --dry-run` report: the Plan, the
// network mode that would apply, whether enforcement is possible here, and
// exactly which files the boundary would hide or mask. It walks the tree, so
// it is computed only on request, never on every launch.
type Inspection struct {
	Plan
	// NetMode is the resolved network mode ("host" or "none") and NetSource
	// names where it came from, so an operator can tell a repo default from
	// their own settings.
	NetMode     string      `json:"netMode"`
	NetSource   string      `json:"netSource"`
	Enforcement Enforcement `json:"enforcement"`

	FileCount int           `json:"fileCount"`
	DirCount  int           `json:"dirCount"`
	Hidden    []check.Match `json:"hidden"`
	Masked    []check.Match `json:"masked"`
	// GitStageable lists Masked files git would stage as-is: the one way a
	// confined agent can still write masked bytes into durable state.
	GitStageable []string `json:"gitStageable"`
	// Findings are the policy linter's own complaints (bad regex, globs that
	// can never match); a boundary with errors in it fails closed, which is
	// worth knowing before launching.
	Findings []check.Finding `json:"findings"`
}

// Inspect builds the dry-run report for plan. netMode/netSource come from
// the CLI's resolution of flag, repo policy, settings, and env.
func Inspect(plan Plan, netMode, netSource string) (Inspection, error) {
	ins := Inspection{Plan: plan, NetMode: netMode, NetSource: netSource}

	if err := nsexec.Supported(); err != nil {
		ins.Enforcement = Enforcement{Reason: err.Error()}
	} else {
		ins.Enforcement = Enforcement{Available: true}
	}

	report, err := check.RunWithOptions(plan.Src, check.Options{Matches: true})
	if err != nil {
		return ins, err
	}
	ins.FileCount = report.FileCount
	ins.DirCount = report.DirCount
	ins.Findings = report.Findings
	for _, m := range report.Matches {
		switch m.Decision {
		case "HIDDEN":
			ins.Hidden = append(ins.Hidden, m)
		case "MASKED":
			ins.Masked = append(ins.Masked, m)
		}
	}

	// Advisory, like the warning exec prints at launch: a failure to detect
	// must not fail the report.
	if hazards, err := check.GitStagingHazards(plan.Src); err == nil {
		ins.GitStageable = hazards
	}
	return ins, nil
}
