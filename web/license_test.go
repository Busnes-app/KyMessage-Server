package web

import (
	"bytes"
	"os"
	"testing"
)

// The About dialog shows src/mit.txt; it must stay the repository's LICENSE.txt.
func TestAboutDialogLicenseMatchesRepository(t *testing.T) {
	repo, err := os.ReadFile("../LICENSE.txt")
	if err != nil {
		t.Fatal(err)
	}
	shown, err := os.ReadFile("src/mit.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(repo, shown) {
		t.Error("web/src/mit.txt differs from LICENSE.txt: copy LICENSE.txt over it")
	}
}
