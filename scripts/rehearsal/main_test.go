//go:build rehearsal

package main

import "testing"

func TestScratchAcceptsOnlyPathsBelowTemp(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	t.Chdir("/") // "relative" must resolve outside /tmp wherever the repo lives
	for _, tc := range []struct {
		path, want string
	}{
		{"/tmp", ""},
		{"/tmp/", ""},
		{"/tmp/rehearsal-1", "/tmp/rehearsal-1"},
		{"/tmp/a/b", "/tmp/a/b"},
		{"/tmpfoo", ""},
		{"/tmp/../etc", ""},
		{"/tmp/a/../../etc", ""},
		{"relative", ""},
	} {
		got, err := scratch(tc.path)
		if tc.want == "" && err == nil {
			t.Errorf("scratch(%q) = %q, want refusal", tc.path, got)
		}
		if tc.want != "" && (err != nil || got != tc.want) {
			t.Errorf("scratch(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
}
