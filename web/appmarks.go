package web

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// The vendored ky-ui marks, so Element's home page (whose sanitiser drops data: images) can
// load them over https from the console.
//
//go:embed src/ky-ui/appmarks.js
var appMarksJS string

// Mark is one app mark's image.
type Mark struct {
	ContentType string
	Body        []byte
}

var markEntry = regexp.MustCompile(`'([a-z0-9-]+)': 'data:([a-z/+]+)(;base64)?,([^']*)'`)

var marks = sync.OnceValues(func() (map[string]Mark, error) {
	out := map[string]Mark{}
	for _, m := range markEntry.FindAllStringSubmatch(appMarksJS, -1) {
		var body []byte
		var err error
		if m[3] != "" {
			body, err = base64.StdEncoding.DecodeString(m[4])
		} else {
			var s string
			s, err = url.PathUnescape(m[4])
			body = []byte(s)
		}
		if err != nil {
			return nil, fmt.Errorf("ky-ui appmarks.js: %s: %w", m[1], err)
		}
		out[m[1]] = Mark{m[2], body}
	}
	return out, nil
})

var markLetter = regexp.MustCompile(`(?i)^(?:ky)?([a-z0-9])`)

// AppMark is ky-ui's appMark(id): the app's mark, or the same letter badge for an unknown ID.
func AppMark(id string) (Mark, error) {
	all, err := marks()
	if err != nil {
		return Mark{}, err
	}
	if m, ok := all[id]; ok {
		return m, nil
	}
	letter := "?"
	if m := markLetter.FindStringSubmatch(id); m != nil {
		letter = strings.ToUpper(m[1])
	}
	return Mark{"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="11" fill="#182326"/><text x="32" y="43" text-anchor="middle" font-family="system-ui,sans-serif" font-size="30" font-weight="700" fill="#f2efe8">` + letter + `</text></svg>`)}, nil
}
