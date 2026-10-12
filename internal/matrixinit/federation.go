package matrixinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
)

// FederationFile (relative to the output dir) is Synapse's second config file. It owns the
// listeners and the federation allow-list; the console rewrites it in place.
const FederationFile = "federation/federation.yaml"

// MaxFederationServers caps the allow-list.
const MaxFederationServers = 100

// FederationYAML renders Synapse's listeners and federation allow-list. An empty list is
// today's closed server: no federation resource and an allow-list that denies every server.
// The output is JSON, which YAML reads as is, so a name cannot add keys.
func FederationYAML(allow []string) ([]byte, error) {
	names := slices.Clone(allow)
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > MaxFederationServers {
		return nil, fmt.Errorf("at most %d federation servers", MaxFederationServers)
	}
	for _, n := range names {
		if err := ValidServerName(n); err != nil {
			return nil, err
		}
		if _, err := netip.ParseAddr(n); err == nil {
			return nil, fmt.Errorf("server name %q is an IP address", n)
		}
	}
	resources := []string{"client", "openid"}
	if len(names) > 0 {
		resources = append(resources, "federation")
	}
	type resource struct {
		Names    []string `json:"names"`
		Compress bool     `json:"compress"`
	}
	type listener struct {
		Port       int        `json:"port"`
		Type       string     `json:"type"`
		TLS        bool       `json:"tls"`
		XForwarded bool       `json:"x_forwarded"`
		Resources  []resource `json:"resources"`
	}
	b, err := json.MarshalIndent(struct {
		Listeners []listener `json:"listeners"`
		Allow     []string   `json:"federation_domain_whitelist"`
	}{
		Listeners: []listener{{Port: 8008, Type: "http", XForwarded: true, Resources: []resource{{Names: resources}}}},
		Allow:     append([]string{}, names...), // [] when empty: null would allow every server
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte("# Written by KyMessages (Settings > Federation). Edit it there, not here.\n"), append(b, '\n')...), nil
}

// WriteFederation rewrites the file in place: Compose binds this single file into Synapse and
// the app, and a running container keeps the inode it was given.
func WriteFederation(path string, allow []string) error {
	b, err := FederationYAML(allow)
	if err != nil {
		return err
	}
	return writeInPlace(path, b, 0o600)
}

// ensureFederation creates the closed file when absent and keeps an existing one: the console
// owns its content.
func ensureFederation(path string) (created bool, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		b, err := FederationYAML(nil)
		if err != nil {
			return false, err
		}
		return true, replaceFile(path, b, 0o600)
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, errors.New("exists and is not a regular file")
	}
	return false, nil
}
