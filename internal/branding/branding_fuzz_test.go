package branding

import (
	"bytes"
	"encoding/json"
	"maps"
	"testing"
)

// FuzzSetBrand patches arbitrary Element config bodies. A body setBrand accepts comes back
// as valid JSON whose brand ParseBrand reads as the name, with every other top-level key's
// bytes untouched; patching it again is a no-op, which is what lets PatchElementConfig skip
// the write. A body it refuses ParseBrand refuses too.
func FuzzSetBrand(f *testing.F) {
	for _, s := range [][2]string{
		{elementConfig, "Acme Chat"},
		{elementConfig, `Acme "Chat" <ops>`},
		{`{"a": 1}`, "Acme"},
		{`{ }`, "Acme"},
		{`{"brand": "x", "b": {"brand": "nested"}, "brand": "y"}`, "Acme"},
		{`{"brand":"A","x":1,"brand":"B"}`, "B"},
		{`{"brand": 5}`, "Acme"},
		{`{"brand": "KyMessages",`, "Acme"},
		{`["brand"]`, "Acme"},
		{`"brand"`, "Acme"},
		{`{"brand":"KyMessages"} {}`, "Acme"},
		{``, "Acme"},
		// Edge cases: escaped key, whitespace around the object, odd names.
		{`{"brand":"x"}`, "Acme"},
		{" \n\t{\"brand\" : null }\n", "Ünïcode 会話 🙂"},
		{`{"a":{"b":[1,2,{"brand":"z"}]}}`, " </script>"},
		{`{}`, "Acme\xff"},
	} {
		f.Add([]byte(s[0]), s[1])
	}
	f.Fuzz(func(t *testing.T, body []byte, name string) {
		out, err := setBrand(bytes.Clone(body), name)
		if err != nil {
			if _, perr := ParseBrand(body); perr == nil {
				t.Fatalf("setBrand refused %q (%v) but ParseBrand accepted it", body, err)
			}
			return
		}
		if !json.Valid(out) {
			t.Fatalf("setBrand(%q) produced invalid JSON %q", body, out)
		}
		// json.Marshal writes invalid UTF-8 as U+FFFD, so that is what reads back.
		var want string
		if b, err := json.Marshal(name); err != nil || json.Unmarshal(b, &want) != nil {
			t.Fatalf("name %q does not survive json", name)
		}
		if got, err := ParseBrand(out); err != nil || got != want {
			t.Fatalf("ParseBrand(setBrand(%q, %q)) = %q, %v", body, name, got, err)
		}
		var before, after map[string]json.RawMessage
		if json.Unmarshal(body, &before) != nil || json.Unmarshal(out, &after) != nil {
			t.Fatalf("accepted body or its patch is not an object: %q -> %q", body, out)
		}
		delete(before, "brand")
		delete(after, "brand")
		if !maps.EqualFunc(before, after, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
			t.Fatalf("setBrand(%q) changed another key: %q", body, out)
		}
		if again, err := setBrand(bytes.Clone(out), name); err != nil || !bytes.Equal(again, out) {
			t.Fatalf("second setBrand is not a no-op: %q -> %q (%v)", out, again, err)
		}
	})
}
