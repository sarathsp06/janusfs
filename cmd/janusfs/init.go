package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/rules"
)

// policyTemplate is the secure-default JanusFS policy: common key material is
// hidden, while secret-bearing config files are preserved but redacted.
const policyTemplate = `# JanusFS policy. Preview with: janusfs check
version: 1

hide:
  - "*.pem"
  - "*.key"
  - id_rsa*
  - "*.p12"
  - .aws/credentials
  - "*.keychain"

mask:
  - paths:
      - "*.env*"
    patterns:
      - env-value

  - paths:
      - "**/application*.yml"
      - "**/application*.yaml"
      - "**/application*.properties"
    patterns:
      - generic-secret
      - db-uri
`

func newInitCmd() *cobra.Command {
	var force bool
	var global bool

	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Generate a template .janusfs.yml policy with secure defaults",
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
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing .janusfs.yml file")
	cmd.Flags().BoolVar(&global, "global", false, "write to ~/.janusfs/config instead, applied to every mount")
	return cmd
}

// runInit writes the secure-default templates, refusing to overwrite without
// --force, and explains what it wrote and why.
func runInit(dir string, force bool) error {
	policyPath := filepath.Join(dir, rules.PolicyFileName)

	if err := writeTemplate(policyPath, policyTemplate, force); err != nil {
		return fmt.Errorf("init: %w", err)
	}

	// Keep this short: what was written, why, and where to look next.
	fmt.Printf("Wrote %s — hides key material and masks .env files plus Spring-style application config\n", policyPath)
	fmt.Printf("  (application*.yml/.yaml/.properties) where secrets commonly live.\n")
	fmt.Println("Targeted by design: add lines like `**/* : aws-key` only if you want a")
	fmt.Println("repo-wide secret scan (it masks every file — slower, noisier).")
	fmt.Println("Run `janusfs check` to preview which files these rules affect before mounting.")
	return nil
}

// runInitGlobal implements the --global variant: the same secure-default
// templates, written to ~/.janusfs/config so they apply to every mount as the
// lowest-precedence level, overridable per-repo as usual.
func runInitGlobal(force bool) error {
	dir, err := rules.GlobalDir()
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("init: creating %s: %w", dir, err)
	}

	policyPath := filepath.Join(dir, rules.PolicyFileName)

	if err := writeTemplate(policyPath, policyTemplate, force); err != nil {
		return fmt.Errorf("init: %w", err)
	}

	fmt.Printf("Wrote %s — applied to every mount, lowest precedence (any repo's own\n", policyPath)
	fmt.Println("  .janusfs.yml can override these).")
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
