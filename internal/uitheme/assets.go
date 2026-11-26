// Package uitheme embeds the shared console components and generated D theme.
package uitheme

import (
	"embed"
	"net/http"
)

//go:embed tokens.generated.css state-labels.generated.js components.css components.js
var assets embed.FS

//go:embed brand/zhiqi-seal-mark.png
var sealMark []byte

// SealMark returns the approved public brand image for the split console.
func SealMark() []byte { return sealMark }

// ServeSealMark serves the approved public brand image on an explicitly registered route.
func ServeSealMark(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(sealMark)
}

// Asset exposes only the four public, credential-free UI assets.
func Asset(name string) ([]byte, bool) {
	switch name {
	case "tokens.generated.css", "state-labels.generated.js", "components.css", "components.js":
		b, err := assets.ReadFile(name)
		return b, err == nil
	default:
		return nil, false
	}
}
