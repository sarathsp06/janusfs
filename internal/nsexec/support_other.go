//go:build !linux

// Package nsexec is the Linux private-mount-namespace machinery behind
// `janusfs exec`. On any other platform, private mount namespaces don't
// exist as a kernel feature, so Supported always fails — there is nothing to
// preflight-check because there is no configuration under which this could
// work.
package nsexec

import "errors"

// Supported always fails on non-Linux platforms.
func Supported() error {
	return errors.New("nsexec: private mount namespaces are a Linux-only kernel feature")
}
