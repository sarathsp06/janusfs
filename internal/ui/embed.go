// Package ui embeds the static dashboard assets via embed.FS.
// The dashboard is a single-page HTML application with inline CSS + JS, plus
// a vendored CodeMirror editor for the config/reveal views — no external
// dependencies, no CDN requests, fully offline-capable.
package ui

import "embed"

//go:embed index.html vendor
var FS embed.FS
