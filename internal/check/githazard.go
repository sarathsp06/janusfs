package check

import (
	"bytes"
	"os/exec"
	"strings"

	"github.com/sarathsp06/janusfs/internal/engine"
)

// GitStagingHazards returns the relative paths under root that resolve Masked
// AND that git would stage as-is: tracked files, plus untracked files not
// covered by .gitignore. `git add` of such a file inside a JanusFS view stages
// the masked bytes ("****") into the real object store — silent data loss the
// moment it is committed. Callers surface this loudly before an agent with
// commit access is put behind the mount.
//
// Returns (nil, nil) when root is not a git work tree or git is not
// installed: no git, no staging hazard. This function shells out to git and
// is called only from short-lived CLI paths (check, exec), never from the
// daemon or the conflicts.json virtual file.
func GitStagingHazards(root string) ([]string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, nil
	}
	cmd := exec.Command(gitPath, "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		// Not a work tree (exit 128) or any other git failure: report no
		// hazards rather than failing the caller — the hazard check is
		// advisory, the caller's real job (check/exec) must still proceed.
		return nil, nil
	}

	eng, err := engine.New(root)
	if err != nil {
		return nil, err
	}

	var hazards []string
	for _, p := range bytes.Split(out, []byte{0}) {
		rel := string(p)
		if rel == "" {
			continue
		}
		rel = strings.TrimSuffix(rel, "/")
		if eng.Resolve(rel, false).Decision == engine.Masked {
			hazards = append(hazards, rel)
		}
	}
	return hazards, nil
}
