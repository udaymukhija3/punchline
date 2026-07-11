package httpapi

import (
	"net/http"
	"os"
	"path"
	"strings"
)

// staticDir resolves the directory of built frontend assets to serve, if any.
// It honours STATIC_DIR and otherwise falls back to ./web when present.
func staticDir() string {
	if d := os.Getenv("STATIC_DIR"); d != "" {
		return d
	}
	if info, err := os.Stat("web"); err == nil && info.IsDir() {
		return "web"
	}
	return ""
}

// spaHandler serves static files from dir and falls back to index.html for
// unknown paths so a single-page app survives deep links and refreshes. It
// relies on http.Dir, which rejects path traversal.
func spaHandler(dir string) http.Handler {
	root := http.Dir(dir)
	fs := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := r.URL.Path
		if !strings.HasPrefix(upath, "/") {
			upath = "/" + upath
		}
		if f, err := root.Open(path.Clean(upath)); err == nil {
			_ = f.Close()
			if strings.HasPrefix(upath, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fs.ServeHTTP(w, r)
			return
		}
		// Missing file -> serve the SPA shell.
		w.Header().Set("Cache-Control", "no-cache")
		r = r.Clone(r.Context())
		r.URL.Path = "/"
		fs.ServeHTTP(w, r)
	})
}
