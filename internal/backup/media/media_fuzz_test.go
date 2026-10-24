package media

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// FuzzDecodeIndex seals arbitrary index bodies under the media key, as a backup directory
// someone else wrote could hold. Every path an accepted index names stays inside a kept
// subtree of the media store once joined to it, and the index re-encodes to itself. The
// same bytes with one bit flipped, or sealed as a file instead of the index, never open.
func FuzzDecodeIndex(f *testing.F) {
	for _, s := range []string{
		`{"local_content/ab/cd/one":{"size":12,"mtime":1},"local_thumbnails/ab/cd/t1":{"size":5,"mtime":2}}`,
		`{}`, `null`,
		`{"../escape":{}}`, `{"local_content/../../x":{}}`, `{"/etc/passwd":{}}`, `{"remote_content/x":{}}`,
		// Edge cases: a bare subtree, dot segments, backslashes, NUL, a trailing slash.
		`{"local_content":{}}`, `{"local_content/./a":{}}`, `{"local_content/a\\..\\..\\b":{}}`,
		`{"local_content/a\u0000b":{}}`, `{"local_content/a/":{}}`, `{"local_content//a":{}}`,
		`{"local_content/a":{"size":-1,"mtime":-1}}`, `[]`, `{"local_content/a":1}`,
	} {
		f.Add([]byte(s), uint16(0))
	}
	a, err := newAEAD(key)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, plain []byte, flip uint16) {
		sealed := seal(a, plain, indexAAD)

		tampered := bytes.Clone(sealed)
		tampered[int(flip/8)%len(tampered)] ^= 1 << (flip % 8)
		if _, err := decodeIndex(a, tampered); err == nil {
			t.Fatalf("index with bit %d flipped opened", flip)
		}
		if _, err := decodeIndex(a, seal(a, plain, fileAAD)); err == nil {
			t.Fatal("a file ciphertext opened as the index")
		}

		idx, err := decodeIndex(a, sealed)
		if err != nil {
			if idx != nil {
				t.Fatalf("decodeIndex failed (%v) but returned %v", err, idx)
			}
			return
		}
		root := filepath.Join(string(filepath.Separator), "srv", "media")
		for rel := range idx {
			p := filepath.Join(root, filepath.FromSlash(rel))
			inside, err := filepath.Rel(root, p)
			if err != nil || !filepath.IsLocal(inside) || inside != filepath.FromSlash(rel) {
				t.Fatalf("index entry %q resolves to %q, outside %q", rel, p, root)
			}
			top, _, _ := strings.Cut(rel, "/")
			if !slices.Contains(Sources, top) || top == rel {
				t.Fatalf("index entry %q is not a file under a kept subtree", rel)
			}
		}
		b, err := json.Marshal(idx)
		if err != nil {
			t.Fatal(err)
		}
		again, err := decodeIndex(a, seal(a, b, indexAAD))
		if err != nil || len(again) != len(idx) {
			t.Fatalf("re-encoded index does not decode: %v", err)
		}
		for rel, e := range idx {
			if again[rel] != e {
				t.Fatalf("%q: %v became %v", rel, e, again[rel])
			}
		}
	})
}
