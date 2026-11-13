// Package uitheme embeds the shared console components and generated D theme.
package uitheme

import "embed"

//go:embed tokens.generated.css state-labels.generated.js components.css components.js
var assets embed.FS

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
