package web

import (
	"bytes"
	"strings"
	"testing"
)

// Every mark in the vendored appmarks.js parses, so a ky-ui refresh cannot drop one silently.
func TestAppMarksParseEveryEntry(t *testing.T) {
	all, err := marks()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Count(appMarksJS, "': 'data:"); len(all) != want || want < 10 {
		t.Fatalf("parsed %d marks of %d", len(all), want)
	}
	for id, m := range all {
		switch m.ContentType {
		case "image/png":
			if !bytes.HasPrefix(m.Body, []byte("\x89PNG\r\n\x1a\n")) {
				t.Errorf("%s: not a PNG", id)
			}
		case "image/svg+xml":
			if !bytes.HasPrefix(m.Body, []byte("<svg ")) || !bytes.HasSuffix(m.Body, []byte("</svg>")) {
				t.Errorf("%s: not an SVG: %.40q", id, m.Body)
			}
		default:
			t.Errorf("%s: type %q", id, m.ContentType)
		}
	}
}
