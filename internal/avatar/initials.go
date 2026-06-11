// Package avatar generates, stores, and serves agent avatars (spec §6.1):
// an async image-backend pipeline with a deterministic initials-SVG fallback.
package avatar

import (
	"fmt"
	"html"
	"strings"
)

// palette mirrors web/src/components/AvatarBubble.tsx exactly — the same
// FNV-1a hash and colors keep the server-generated fallback identical to the
// bubble the client renders before avatar_url is set.
var palette = []string{"#e06c75", "#e5c07b", "#98c379", "#56b6c2", "#61afef", "#c678dd", "#d19a66", "#be8c6c"}

// fnv32 is FNV-1a over the name's code points (matches the TS implementation,
// which iterates code points and uses 32-bit Math.imul).
func fnv32(name string) uint32 {
	h := uint32(2166136261)
	for _, r := range name {
		h ^= uint32(r)
		h *= 16777619
	}
	return h
}

// initials extracts 1–2 uppercase initials: first rune of the first word and
// first rune of the last word ("?" when the name is blank).
func initials(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	first := firstRune(parts[0])
	if len(parts) == 1 {
		return strings.ToUpper(first)
	}
	return strings.ToUpper(first + firstRune(parts[len(parts)-1]))
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// InitialsSVG renders the deterministic 256×256 fallback avatar.
func InitialsSVG(name string) []byte {
	color := palette[fnv32(name)%uint32(len(palette))]
	text := html.EscapeString(initials(name))
	return []byte(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256" viewBox="0 0 256 256">`+
			`<rect width="256" height="256" fill="%s"/>`+
			`<text x="50%%" y="50%%" dy="0.35em" text-anchor="middle" font-family="sans-serif" font-size="102" font-weight="700" fill="#ffffff">%s</text>`+
			`</svg>`, color, text))
}
