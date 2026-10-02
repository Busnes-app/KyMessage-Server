// Command textpng makes and checks the acceptance harness's logo fixture. With no argument it
// writes to stdout a 64x64 PNG carrying a tEXt chunk whose text holds a marker; with -check FILE
// it fails unless FILE decodes as a PNG and holds no text chunk and no marker. Harness only.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
)

const marker = "kymatrix-logo-comment"

func main() {
	switch {
	case len(os.Args) == 1:
		if _, err := os.Stdout.Write(fixture()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case len(os.Args) == 3 && os.Args[1] == "-check":
		if err := check(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: textpng [-check FILE]")
		os.Exit(2)
	}
}

func fixture() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range 64 {
		img.Set(i, i, color.NRGBA{R: 191, G: 63, B: 24, A: 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img) // a valid in-memory image always encodes
	p := b.Bytes()
	data := []byte("Comment\x00" + marker)
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(c, "tEXt"...)
	c = append(c, data...)
	c = binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(append([]byte("tEXt"), data...)))
	// After the signature (8 bytes) and IHDR (25 bytes).
	return append(append(append([]byte{}, p[:33]...), c...), p[33:]...)
}

func check(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%s does not decode as a PNG: %w", path, err)
	}
	for _, bad := range []string{"tEXt", "zTXt", "iTXt", marker} {
		if bytes.Contains(b, []byte(bad)) {
			return fmt.Errorf("%s still carries %q", path, bad)
		}
	}
	fmt.Printf("%dx%d\n", img.Bounds().Dx(), img.Bounds().Dy())
	return nil
}
