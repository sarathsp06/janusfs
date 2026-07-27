//go:build !linux

package main

import "github.com/spf13/cobra"

// registerPlatformCommands is a no-op on non-Linux platforms: the __nsmount
// hidden command (Stage 2 of the Linux namespace-exec path) only makes sense
// where private mount namespaces exist. See nsmount_linux.go for the Linux
// implementation.
func registerPlatformCommands(root *cobra.Command) {}
