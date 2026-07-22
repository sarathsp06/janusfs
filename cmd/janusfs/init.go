package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/rules"
)

// janusignoreTemplate is FR-17's secure-default .janusignore: standard
// secrets, keypairs, and credential stores hidden out of the box.
const janusignoreTemplate = `# JanusFS — paths listed here are Hidden: they appear in listings but
# every read/write is denied (EACCES). Syntax mirrors .gitignore exactly.
#
# Preview the effect of these rules with: janusfs check

*.pem
*.key
id_rsa*
*.p12
.aws/credentials
*.keychain
`

// janusmaskTemplate is FR-17's secure-default .janusmask: common
// secret-bearing file shapes, masked with the built-in pattern library.
const janusmaskTemplate = `# JanusFS — <glob> : <pattern>[, <pattern>...] masks matched spans with
# '*', preserving file size. A glob with no pattern masks the whole file.
#
# Preview the effect of these rules with: janusfs check

*.env*                              : env-value
**/application*.yml                 : generic-secret, db-uri
**/application*.yaml                : generic-secret, db-uri
**/application*.properties          : generic-secret, db-uri
`

func newInitCmd() *cobra.Command {
	var force bool
	var global bool

	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Generate template .janusignore and .janusmask files with secure defaults",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if global {
				if len(args) == 1 {
					return fmt.Errorf("init: --global does not take a [dir] argument (it always targets ~/.janusfs/config)")
				}
				return runInitGlobal(force)
			}
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runInit(dir, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing .janusignore/.janusmask files")
	cmd.Flags().BoolVar(&global, "global", false, "write to ~/.janusfs/config instead, applied to every mount (docs/SPEC_AMENDMENTS.md 2026-07-17)")
	return cmd
}

// runInit implements FR-17 (template generation, refuse to overwrite
// without --force) and FR-32 (explain what was written and why, in ≤ 10
// lines).
func runInit(dir string, force bool) error {
	ignorePath := filepath.Join(dir, ".janusignore")
	maskPath := filepath.Join(dir, ".janusmask")

	if err := writeTemplate(ignorePath, janusignoreTemplate, force); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := writeTemplate(maskPath, janusmaskTemplate, force); err != nil {
		return fmt.Errorf("init: %w", err)
	}

	// FR-32: ≤ 10 lines, explaining what and why, pointing at `check`.
	fmt.Printf("Wrote %s — hides *.pem/*.key/id_rsa*/*.p12/.aws credentials/*.keychain by default.\n", ignorePath)
	fmt.Printf("Wrote %s — masks .env files and Spring-style application config\n", maskPath)
	fmt.Printf("  (application*.yml/.yaml/.properties) where secrets commonly live.\n")
	fmt.Println("Targeted by design: add lines like `**/* : aws-key` only if you want a")
	fmt.Println("repo-wide secret scan (it masks every file — slower, noisier).")
	fmt.Println("Run `janusfs check` to preview which files these rules affect before mounting.")
	return nil
}

// runInitGlobal implements the --global variant added by
// docs/SPEC_AMENDMENTS.md (2026-07-17): the same secure-default templates,
// written to ~/.janusfs/config so they apply to every mount as the
// lowest-precedence level, overridable per-repo as usual.
func runInitGlobal(force bool) error {
	dir, err := rules.GlobalDir()
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("init: creating %s: %w", dir, err)
	}

	ignorePath := filepath.Join(dir, ".janusignore")
	maskPath := filepath.Join(dir, ".janusmask")

	if err := writeTemplate(ignorePath, janusignoreTemplate, force); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := writeTemplate(maskPath, janusmaskTemplate, force); err != nil {
		return fmt.Errorf("init: %w", err)
	}

	fmt.Printf("Wrote %s and %s — applied to every mount, lowest precedence (any repo's own\n", ignorePath, maskPath)
	fmt.Println("  .janusignore/.janusmask can override these).")
	fmt.Println("Run `janusfs check <dir>` on any repo to preview the combined effect.")
	return nil
}

func writeTemplate(path, content string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
