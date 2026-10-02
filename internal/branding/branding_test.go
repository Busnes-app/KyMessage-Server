package branding

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateName(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Chat":             "Acme Chat",
		"  Acme  ":              "Acme",
		strings.Repeat("é", 64): strings.Repeat("é", 64),
		"Ünïcode 会話 🙂":          "Ünïcode 会話 🙂",
	} {
		if got, err := ValidateName(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for name, in := range map[string]string{
		"empty":            "",
		"blank":            " \t ",
		"65 characters":    strings.Repeat("é", 65),
		"newline":          "Acme\nChat",
		"tab":              "Acme\tChat",
		"NUL":              "Acme\x00",
		"DEL":              "Acme\x7f",
		"C1 control (NEL)": "Ac\u0085me",
		"bidi override":    "‮Acme",
		"bidi isolate":     "Acme⁦x",
		"zero-width space": "Ac​me",
		"ZWJ":              "Ac‍me",
		"BOM":              "\ufeffAcme",
		"line separator":   "Acme Chat",
		"invalid UTF-8":    "Acme\xff",
	} {
		if _, err := ValidateName(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func encode(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 191, G: 63, B: 24, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// chunk is one PNG chunk: length, type, data, and the CRC over type and data.
func chunk(typ string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, typ...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(typ), data...)))
}

// afterIHDR inserts c after the signature (8 bytes) and the IHDR chunk (25 bytes).
func afterIHDR(p, c []byte) []byte {
	return append(append(append([]byte{}, p[:33]...), c...), p[33:]...)
}

// withSize rewrites IHDR's width and height and fixes its CRC; the pixels stay as they were.
func withSize(p []byte, w, h uint32) []byte {
	p = append([]byte{}, p...)
	binary.BigEndian.PutUint32(p[16:], w)
	binary.BigEndian.PutUint32(p[20:], h)
	binary.BigEndian.PutUint32(p[29:], crc32.ChecksumIEEE(p[12:29]))
	return p
}

func TestNormalizePNGStripsAncillaryChunks(t *testing.T) {
	in := afterIHDR(encode(t, 16, 16), chunk("tEXt", []byte("Comment\x00kymatrix-secret")))
	if _, err := png.Decode(bytes.NewReader(in)); err != nil {
		t.Fatalf("fixture does not decode: %v", err)
	}
	out, err := NormalizePNG(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("tEXt")) || bytes.Contains(out, []byte("kymatrix-secret")) {
		t.Fatal("the tEXt chunk survived")
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 16 || img.Bounds().Dy() != 16 {
		t.Fatalf("re-encoding does not decode as the same image: %v", err)
	}
	if _, err := NormalizePNG(encode(t, MaxLogoSide, MaxLogoSide)); err != nil {
		t.Fatalf("1024×1024 refused: %v", err)
	}
}

func TestNormalizePNGRefuses(t *testing.T) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	small := encode(t, 4, 4)
	oversize := make([]byte, MaxLogoBytes+1)
	copy(oversize, small)
	for name, tc := range map[string]struct {
		in   []byte
		want error
	}{
		"JPEG":                     {jpg.Bytes(), ErrNotPNG},
		"SVG":                      {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ErrNotPNG},
		"empty":                    {nil, ErrNotPNG},
		"signature only":           {small[:8], ErrNotPNG},
		"truncated pixels":         {small[:len(small)-20], ErrNotPNG},
		"zero width":               {withSize(small, 0, 4), ErrNotPNG},
		"1025 px wide":             {encode(t, 1025, 1), ErrLogoDimensions},
		"1025 px high":             {encode(t, 1, 1025), ErrLogoDimensions},
		"bomb: header says 50000²": {withSize(small, 50000, 50000), ErrLogoDimensions},
		"1 MiB + 1 byte":           {oversize, ErrLogoTooLarge},
	} {
		if _, err := NormalizePNG(tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

const elementConfig = `{
  "default_server_config": {
    "m.homeserver": { "base_url": "https://matrix.example.com" }
  },
  "brand": "KyMessages",
  "branding": { "auth_header_logo_url": "https://admin.example.com/app-icon.png" },
  "setting_defaults": { "UIFeature.voip": false }
}
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPatchElementBrandChangesOnlyTheBrandInPlace(t *testing.T) {
	p := writeConfig(t, elementConfig)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	name := `Acme "Chat" <ops>`
	changed, err := PatchElementBrand(p, name)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(p)
	if want := strings.Replace(elementConfig, `"KyMessages"`, `"Acme \"Chat\" \u003cops\u003e"`, 1); string(got) != want {
		t.Fatalf("other bytes changed:\n%s", got)
	}
	after, _ := os.Stat(p)
	if !os.SameFile(before, after) {
		t.Fatal("the file was replaced, not rewritten in place")
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %04o", after.Mode().Perm())
	}
	if brand, err := ElementBrand(p); err != nil || brand != name {
		t.Fatalf("read back %q, %v", brand, err)
	}
	// Idempotent: the same name again writes nothing.
	old := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if changed, err := PatchElementBrand(p, name); err != nil || changed {
		t.Fatalf("second patch: changed=%v err=%v", changed, err)
	}
	if fi, _ := os.Stat(p); !fi.ModTime().Equal(old) {
		t.Fatal("an unchanged brand was rewritten")
	}
}

func TestPatchElementBrandAddsAMissingKeyAndPatchesDuplicates(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1}`: `{"brand":"Acme","a": 1}`,
		`{ }`:      `{"brand":"Acme" }`,
		`{"brand": "x", "b": {"brand": "nested"}, "brand": "y"}`: `{"brand": "Acme", "b": {"brand": "nested"}, "brand": "Acme"}`,
	} {
		p := writeConfig(t, in)
		if _, err := PatchElementBrand(p, "Acme"); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	if brand, err := ElementBrand(writeConfig(t, `{"a": 1}`)); err != nil || brand != "" {
		t.Errorf("absent brand: %q, %v", brand, err)
	}
	if _, err := ElementBrand(writeConfig(t, `{"brand": 5}`)); err == nil {
		t.Error("a non-string brand was read")
	}
}

func TestPatchElementBrandRefusesAndLeavesTheFileAlone(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.json")
	if _, err := PatchElementBrand(missing, "Acme"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("a missing config was created")
	}
	if _, err := PatchElementBrand(t.TempDir(), "Acme"); err == nil {
		t.Error("a directory was accepted")
	}
	for name, body := range map[string]string{
		"unparseable": `{"brand": "KyMessages",`,
		"array":       `["brand"]`,
		"string":      `"brand"`,
		"trailing":    `{"brand":"KyMessages"} {}`,
		"empty":       ``,
	} {
		p := writeConfig(t, body)
		if _, err := PatchElementBrand(p, "Acme"); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Errorf("%s: file changed to %q", name, got)
		}
	}
}
