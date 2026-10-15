// Package agentskill ships the KyMessages skill for chat agents (OpenClaw, Hermes Agent) and
// fills each runtime's config block from the skill's own reference file, so the console and
// the skill can never disagree.
package agentskill

import (
	"archive/zip"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed kymessages
var files embed.FS

// Runtimes are the agent runtimes with a reference file and a config block.
var Runtimes = []string{"openclaw", "hermes"}

// Values fill a config block's placeholders.
type Values struct {
	Homeserver, AccessToken, Owner string
}

// Config returns runtime's config block with v filled in.
func Config(runtime string, v Values) (string, error) {
	block, err := configBlock(runtime)
	if err != nil {
		return "", err
	}
	for _, s := range []string{v.Homeserver, v.AccessToken, v.Owner} {
		// Each value lands inside a quoted string or an env line.
		if s == "" || strings.ContainsAny(s, "\"'\\\r\n\t #`$") {
			return "", fmt.Errorf("unusable config value %q", s)
		}
	}
	return strings.NewReplacer("<HOMESERVER>", v.Homeserver, "<ACCESS_TOKEN>", v.AccessToken, "<OWNER>", v.Owner).Replace(block), nil
}

// configBlock is the first fenced block of the runtime's reference file.
func configBlock(runtime string) (string, error) {
	md, err := files.ReadFile("kymessages/references/" + runtime + ".md")
	if err != nil {
		return "", fmt.Errorf("unknown agent runtime %q", runtime)
	}
	_, rest, ok := strings.Cut(string(md), "\n```")
	if ok {
		_, rest, ok = strings.Cut(rest, "\n") // the fence's info string
	}
	block, _, closed := strings.Cut(rest, "```")
	if !ok || !closed {
		return "", errors.New("reference has no config block")
	}
	return block, nil
}

// Zip is the skill folder as a zip with kymessages/ at its root.
func Zip() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := fs.WalkDir(files, "kymessages", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
