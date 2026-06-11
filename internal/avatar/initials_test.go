package avatar

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestInitialsSVGDeterministic(t *testing.T) {
	a := InitialsSVG("Ada Lovelace")
	b := InitialsSVG("Ada Lovelace")
	if !bytes.Equal(a, b) {
		t.Error("same name produced different SVGs")
	}
	if !strings.Contains(string(a), ">AL<") {
		t.Errorf("SVG missing initials AL: %s", a)
	}
}

func TestInitialsSVGDistinctColors(t *testing.T) {
	a := string(InitialsSVG("Ada Lovelace"))
	g := string(InitialsSVG("Grace Hopper"))
	colorOf := func(svg string) string {
		i := strings.Index(svg, `fill="`)
		return svg[i+6 : i+13]
	}
	if colorOf(a) == colorOf(g) {
		t.Errorf("expected distinct palette colors, both got %s", colorOf(a))
	}
}

func TestInitialsSVGValidXML(t *testing.T) {
	for _, name := range []string{"Ada Lovelace", "solo", "", "  ", "Ünïcôde Ñame", "a & <b>"} {
		var v struct{}
		if err := xml.Unmarshal(InitialsSVG(name), &v); err != nil {
			t.Errorf("InitialsSVG(%q) produced invalid XML: %v", name, err)
		}
	}
}

func TestInitialsExtraction(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Ada Lovelace", "AL"},
		{"solo", "S"},
		{"three word name", "TN"},
		{"", "?"},
		{"   ", "?"},
		{"über cool", "ÜC"},
	}
	for _, tc := range cases {
		if got := initials(tc.name); got != tc.want {
			t.Errorf("initials(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPaletteMatchesSPA(t *testing.T) {
	// The SPA's AvatarBubble (web/src/components/AvatarBubble.tsx) uses
	// FNV-1a over code points into this exact palette; server fallback must
	// produce the same color for the same name.
	want := []string{"#e06c75", "#e5c07b", "#98c379", "#56b6c2", "#61afef", "#c678dd", "#d19a66", "#be8c6c"}
	if len(palette) != len(want) {
		t.Fatalf("palette size %d, want %d", len(palette), len(want))
	}
	for i := range want {
		if palette[i] != want[i] {
			t.Errorf("palette[%d] = %s, want %s", i, palette[i], want[i])
		}
	}
}
