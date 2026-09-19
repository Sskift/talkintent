package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"strings"
)

// StaticFiles contains the embedded vanilla HTML/JS/CSS assets for the Web UI.
//
//go:embed static/*
var StaticFiles embed.FS

func init() {
	// Ensure standard MIME types are explicitly registered so behavior is
	// consistent regardless of host OS registry settings (e.g. Windows .js mime).
	_ = mime.AddExtensionType(".html", "text/html; charset=utf-8")
	_ = mime.AddExtensionType(".js", "application/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".css", "text/css; charset=utf-8")
	_ = mime.AddExtensionType(".json", "application/json; charset=utf-8")
	_ = mime.AddExtensionType(".svg", "image/svg+xml")
}

// FS returns an fs.FS rooted at the "static" subdirectory,
// so that "index.html", "app.js", and "style.css" are at the root.
// This is suitable for passing to http.FileServer.
func FS() fs.FS {
	sub, err := fs.Sub(StaticFiles, "static")
	if err != nil {
		return StaticFiles
	}
	return sub
}

// Handler returns an http.Handler that serves the embedded web dashboard.
// It can be mounted at "/" or "/web/" or any subpath.
// If the requested path is "/" or "/web" or "/web/" or empty, it serves "index.html".
// It serves static files with correct Content-Type headers for .html, .js, and .css.
func Handler() http.Handler {
	subFS := FS()
	fileServer := http.FileServer(http.FS(subFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Strip /web or /web/ prefix if present
		trimmed := strings.TrimPrefix(path, "/web")
		trimmed = strings.TrimPrefix(trimmed, "/")

		// Handle root or SPA fallback
		if trimmed == "" || trimmed == "index.html" {
			data, err := fs.ReadFile(subFS, "index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}

		if trimmed == "app.js" {
			data, err := fs.ReadFile(subFS, "app.js")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			_, _ = w.Write(data)
			return
		}

		if trimmed == "style.css" {
			data, err := fs.ReadFile(subFS, "style.css")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			_, _ = w.Write(data)
			return
		}

		// Try reading directly from subFS
		if data, err := fs.ReadFile(subFS, trimmed); err == nil {
			dotIdx := strings.LastIndex(trimmed, ".")
			ctype := ""
			if dotIdx >= 0 {
				ctype = mime.TypeByExtension(trimmed[dotIdx:])
			}
			if ctype == "" {
				ctype = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ctype)
			_, _ = w.Write(data)
			return
		}

		// If path has no file extension (e.g. SPA subroute), fallback to index.html
		if !strings.Contains(trimmed, ".") {
			if data, err := fs.ReadFile(subFS, "index.html"); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(data)
				return
			}
		}

		// Delegate to stdlib FileServer with cloned request path
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + trimmed
		fileServer.ServeHTTP(w, r2)
	})
}
