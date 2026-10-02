// Package branding holds the pure checks and the one file edit behind the console's product
// name and logo: name validation, PNG normalisation and Element's brand patch.
package branding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxNameRunes bounds the product name, in characters after trimming.
	MaxNameRunes = 64
	// MaxLogoBytes bounds an upload and its re-encoding.
	MaxLogoBytes = 1 << 20
	// MaxLogoSide bounds each dimension, checked before any pixel is decoded.
	MaxLogoSide = 1024
)

var (
	ErrNotPNG         = errors.New("the logo is not a PNG image")
	ErrLogoTooLarge   = errors.New("the logo is larger than 1 MiB")
	ErrLogoDimensions = fmt.Errorf("the logo must be between 1×1 and %d×%d pixels", MaxLogoSide, MaxLogoSide)
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// ValidateName trims s and accepts 1 to MaxNameRunes characters of valid UTF-8 with no
// control, format (bidi overrides, zero-width characters) or line/paragraph separator
// character: the name lands in page titles and Element's config, where those reorder or hide text.
func ValidateName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", errors.New("the name is not valid UTF-8")
	}
	switch n := utf8.RuneCountInString(s); {
	case n == 0:
		return "", errors.New("the name is empty")
	case n > MaxNameRunes:
		return "", fmt.Errorf("the name is longer than %d characters", MaxNameRunes)
	}
	for _, r := range s {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return "", fmt.Errorf("the name contains an invisible or control character (U+%04X)", r)
		}
	}
	return s, nil
}

// NormalizePNG returns b decoded and re-encoded, so no ancillary chunk (text, EXIF, colour
// profile, animation) survives. The header's dimensions are refused before any pixel is
// decoded, which bounds the decode at MaxLogoSide² pixels whatever the header claims.
func NormalizePNG(b []byte) ([]byte, error) {
	if len(b) > MaxLogoBytes {
		return nil, ErrLogoTooLarge
	}
	if !bytes.HasPrefix(b, pngSignature) {
		return nil, ErrNotPNG
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotPNG
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxLogoSide || cfg.Height > MaxLogoSide {
		return nil, ErrLogoDimensions
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotPNG
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	if out.Len() > MaxLogoBytes {
		return nil, ErrLogoTooLarge
	}
	return out.Bytes(), nil
}

// PatchElementBrand sets the top-level "brand" of Element's config.json at path to name and
// changes no other byte. It writes only when that changes the file, and through the existing
// inode (no create, rename or chmod): a single-file bind mount keeps the inode it was given.
// A missing, non-regular or unparseable file, or one that is not a JSON object, is refused
// untouched. Callers serialise writers; a reader can see the file mid-write.
func PatchElementBrand(path, name string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	patched, err := setBrand(old, name)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if bytes.Equal(patched, old) {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return false, err
	}
	_, err = f.Write(patched)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err == nil, err
}

// ElementBrand reads the top-level "brand" of Element's config.json, "" when absent.
func ElementBrand(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	spans, err := brandSpans(b)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(spans) == 0 {
		return "", nil
	}
	last := spans[len(spans)-1] // JSON.parse keeps the last duplicate
	var v string
	if err := json.Unmarshal(b[last.start:last.end], &v); err != nil {
		return "", fmt.Errorf("%s: brand is not a string", path)
	}
	return v, nil
}

type span struct{ start, end int }

// brandSpans returns the byte range of every top-level "brand" value in b, which must be one
// JSON object.
func brandSpans(b []byte) ([]span, error) {
	if !json.Valid(b) {
		return nil, errors.New("is not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("is not a JSON object")
	}
	var spans []span
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if key == "brand" {
			end := int(dec.InputOffset())
			s := span{end - len(v), end}
			if s.start < 0 || !bytes.Equal(b[s.start:s.end], v) {
				return nil, errors.New("could not locate the brand value")
			}
			spans = append(spans, s)
		}
	}
	return spans, nil
}

// setBrand replaces every top-level brand value with name, or inserts one first.
func setBrand(b []byte, name string) ([]byte, error) {
	spans, err := brandSpans(b)
	if err != nil {
		return nil, err
	}
	v, err := json.Marshal(name)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		i := bytes.IndexByte(b, '{') + 1
		sep := []byte(",")
		if bytes.TrimSpace(b[i:])[0] == '}' {
			sep = nil
		}
		return slices.Concat(b[:i], []byte(`"brand":`), v, sep, b[i:]), nil
	}
	out := b
	for i := len(spans) - 1; i >= 0; i-- {
		out = slices.Concat(out[:spans[i].start], v, out[spans[i].end:])
	}
	return out, nil
}
