package controlled

import (
	"net/http"
	"strings"

	"github.com/xielforever/MCPGuard/internal/uitheme"
)

const consoleSecurityHeaders = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// StaticHandler serves the console page and its public assets without the
// browser-session middleware. It is intended for the separate console
// frontend; the gateway still owns /console/login and /console/api and enforces
// host, Origin, CSRF and identity checks there.
func StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		setConsoleHeaders(w)
		switch r.URL.Path {
		case "/console/", "/web/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(consoleHTML)
		case "/web/console.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(consoleJS)
		case "/web/console.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			_, _ = w.Write(consoleCSS)
		case "/web/ui/zhiqi-seal-mark.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(uitheme.SealMark())
		case "/web/ui/tokens.css", "/web/ui/components.css", "/web/ui/state-labels.js", "/web/ui/components.js":
			name := strings.TrimPrefix(r.URL.Path, "/web/ui/")
			if name == "tokens.css" {
				name = "tokens.generated.css"
			}
			if name == "state-labels.js" {
				name = "state-labels.generated.js"
			}
			asset, ok := uitheme.Asset(name)
			if !ok {
				http.NotFound(w, r)
				return
			}
			mime := "text/javascript; charset=utf-8"
			if strings.HasSuffix(name, ".css") {
				mime = "text/css; charset=utf-8"
			}
			w.Header().Set("Content-Type", mime)
			_, _ = w.Write(asset)
		default:
			http.NotFound(w, r)
		}
	})
}

func setConsoleHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", consoleSecurityHeaders)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
}