package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFS embeds the vendored timeline script (and any future first-party
// static assets) into the binary. This is the dashboard's only client-side
// JavaScript: no CDN, no external network fetch — the script is served
// first-party from this embedded filesystem so script-src 'self' is
// sufficient in the Content-Security-Policy.
//
//go:embed static/timeline.js
var staticFS embed.FS

// NewStaticHandler returns an http.Handler serving the embedded static
// assets (currently just the vendored timeline.js). It is intended to be
// mounted at the "/static/" prefix (see cmd/dashboard/main.go), with the
// same conservative security headers applied to every response.
//
// It only ever serves a regular file that exists in the embedded FS — never
// a directory listing (http.FileServer's default index-of-"/" behavior) and
// never anything outside the embedded set, regardless of how the request
// path is crafted.
func NewStaticHandler() http.Handler {
	fileServer := http.FileServer(http.FS(staticFS))
	inner := http.StripPrefix("/", fileServer)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())

		name := staticAssetName(r.URL.Path)
		info, err := fs.Stat(staticFS, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}

		inner.ServeHTTP(w, r)
	})
}

// staticAssetName strips the leading "/" from the request path so it can be
// looked up in staticFS, whose root already contains the "static/" segment
// (from the go:embed directive). http.StripPrefix in the caller performs the
// equivalent transform for the actual serve; this helper is used purely to
// pre-check existence with fs.Stat before delegating.
func staticAssetName(urlPath string) string {
	if len(urlPath) > 0 && urlPath[0] == '/' {
		return urlPath[1:]
	}
	return urlPath
}
