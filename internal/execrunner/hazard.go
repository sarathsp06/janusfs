package execrunner

import (
	"fmt"
	"os"

	"github.com/sarathsp06/janusfs/internal/check"
)

// warnGitStagingHazards prints a loud stderr warning when files under src are
// Masked AND stageable by git: a child with commit access running `git add`
// through the filtered view stages the masked bytes ("****") into the real
// object store. Best-effort and advisory — a failure to detect must never
// block the exec itself, so errors are swallowed after a terse note.
func warnGitStagingHazards(src string) {
	hazards, err := check.GitStagingHazards(src)
	if err != nil || len(hazards) == 0 {
		return
	}
	const maxShown = 10
	fmt.Fprintf(os.Stderr, "janusfs exec: WARNING: %d masked file(s) are stageable by git — `git add` through this view stages masked bytes, not the real content:\n", len(hazards))
	for i, rel := range hazards {
		if i == maxShown {
			fmt.Fprintf(os.Stderr, "  ... and %d more (run `janusfs check` for the full list)\n", len(hazards)-maxShown)
			break
		}
		fmt.Fprintf(os.Stderr, "  %s\n", rel)
	}
	fmt.Fprintln(os.Stderr, "  Do not let the child commit these paths; add them to .gitignore or commit from outside the view.")
}
