package main

import (
	"bytes"
	"os"
	"testing"
)

// The committed pins must be what go generate writes from the committed Compose file.
func TestPinsMatchCompose(t *testing.T) {
	compose, err := os.ReadFile("../../../docker-compose.matrix.yml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := render(compose)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../pins.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/health/pins.go is stale; run: go generate ./internal/health\n--- want\n%s", want)
	}
}

func TestTagVersion(t *testing.T) {
	for image, want := range map[string]string{
		"postgres:17.6-alpine@sha256:ef25":                        "17.6",
		"ghcr.io/element-hq/synapse:v1.162.0@sha256:6b84":         "1.162.0",
		"ghcr.io/element-hq/matrix-authentication-service:1.26.0": "1.26.0",
		"registry.local:5000/element-web:v1.12.30":                "1.12.30",
	} {
		if got, err := tagVersion(image); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", image, got, err, want)
		}
	}
	for _, image := range []string{"registry.local:5000/element-web", "synapse:latest", "synapse@sha256:6b84", ""} {
		if got, err := tagVersion(image); err == nil {
			t.Errorf("%s accepted as %q", image, got)
		}
	}
}

func TestRenderRequiresEveryService(t *testing.T) {
	if _, err := render([]byte("services:\n  synapse:\n    image: synapse:v1.0.0\n")); err == nil {
		t.Fatal("a Compose file without mas, element and postgres rendered")
	}
}
