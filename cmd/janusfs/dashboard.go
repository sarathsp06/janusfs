package main

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// This file owns the daemon's HTTP dashboard surface: the combined index at
// `/` and the reverse-proxy routing of each mount's own dashboard/API under
// `/mounts/<uuid>/`. It is split out of daemon.go so "how is the dashboard
// served" lives in one place, separate from the control-socket + mount
// lifecycle core.

// handleHTTP multiplexes both the daemon-level combined index view, the individual
// mount dashboards, and their API/SSE/static asset endpoints under `/mounts/<uuid>/`.
func (d *daemon) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		d.handleIndex(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/mounts/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/mounts/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			uuid := parts[0]
			d.mu.Lock()
			var matched *mountRuntime
			for _, rt := range d.mounts {
				if rt.UUID == uuid {
					matched = rt
					break
				}
			}
			d.mu.Unlock()

			if matched != nil && matched.apiSrv != nil {
				// Strip the prefix "/mounts/<uuid>" so that the existing api.Server
				// router handles it correctly (it registers endpoints starting with "/").
				trimmedPath := "/" + strings.Join(parts[1:], "/")
				r.URL.Path = trimmedPath
				matched.apiSrv.ServeHTTP(w, r)
				return
			}
		}
	}

	http.NotFound(w, r)
}

// handleIndex serves the combined dashboard: a plain list of every live
// mount linking to its individual dashboard.
func (d *daemon) handleIndex(w http.ResponseWriter, r *http.Request) {
	mounts := d.snapshot()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprint(w, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>JanusFS</title>")
	_, _ = fmt.Fprint(w, "<style>body{font-family:-apple-system,system-ui,sans-serif;background:#f4efea;color:#383838;max-width:820px;margin:40px auto;padding:0 24px}"+
		"h1{font-size:22px}a{color:#383838}li{margin:8px 0;list-style:none;padding:12px;background:#fff;border:2px solid #383838;box-shadow:-4px 4px 0 #383838}"+
		".mp{font-family:monospace;font-size:13px;color:#666;margin-top:4px;display:flex;gap:8px;align-items:center}"+
		"button.copy{font:inherit;font-size:11px;cursor:pointer;border:1px solid #383838;background:#fff;padding:1px 8px}"+
		"button.copy:hover{background:#ffde00}</style></head><body>")
	_, _ = fmt.Fprintf(w, "<h1>JanusFS — %d mount(s)</h1><ul>", len(mounts))
	if len(mounts) == 0 {
		_, _ = fmt.Fprint(w, "<p>No active mounts. Run <code>janusfs mount &lt;src&gt;</code>.</p>")
	}
	for _, m := range mounts {
		title := m.Label
		if title == "" {
			title = m.Src
		}
		_, _ = fmt.Fprintf(w, "<li><a href=\"%s\">%s</a><div class=\"mp\"><span>%s</span>"+
			"<button class=\"copy\" data-mp=\"%s\">copy path</button></div></li>",
			html.EscapeString(m.Dashboard), html.EscapeString(title),
			html.EscapeString(m.Mountpoint), html.EscapeString(m.Mountpoint))
	}
	_, _ = fmt.Fprint(w, "</ul>")
	_, _ = fmt.Fprint(w, "<script>document.querySelectorAll('button.copy').forEach(function(b){"+
		"b.addEventListener('click',function(){navigator.clipboard.writeText(b.getAttribute('data-mp'))"+
		".then(function(){var t=b.textContent;b.textContent='copied';setTimeout(function(){b.textContent=t;},1500);});});});</script>")
	_, _ = fmt.Fprint(w, "</body></html>")
}
