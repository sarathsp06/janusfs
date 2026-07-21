// Package ui embeds the static dashboard assets via embed.FS (SPEC §11/§20.4).
// The dashboard is a single-page HTML application with inline CSS + JS, plus
// a vendored CodeMirror editor for the config/reveal views — no external
// dependencies, no CDN requests, fully offline-capable (FR-40).
package ui

import "embed"

//go:embed index.html vendor
var FS embed.FS
