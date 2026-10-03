package matrixinit

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func turnInput(t *testing.T) Input {
	t.Helper()
	in := goodInput()
	in.TurnHost = "turn.example.com"
	in.TurnCertFile = filepath.Join(t.TempDir(), "cert.pem")
	in.TurnKeyFile = filepath.Join(t.TempDir(), "key.pem")
	writeTurnPair(t, in, in.TurnHost, time.Now().Add(time.Hour))
	return in
}

func writeTurnPair(t *testing.T, in Input, host string, until time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for path, b := range map[string][]byte{in.TurnCertFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), in.TurnKeyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})} {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTurnTLSRefusedBeforeMutation(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *Input){
		"missing hostname": func(t *testing.T, i *Input) { i.TurnHost = "" },
		"missing cert":     func(t *testing.T, i *Input) { i.TurnCertFile = "" },
		"missing key":      func(t *testing.T, i *Input) { i.TurnKeyFile = "" },
		"bad hostname":     func(t *testing.T, i *Input) { i.TurnHost = "https://turn.example.com" },
		"wrong hostname":   func(t *testing.T, i *Input) { i.TurnHost = "other.example.com" },
		"expired":          func(t *testing.T, i *Input) { writeTurnPair(t, *i, i.TurnHost, time.Now().Add(-time.Minute)) },
		"invalid PEM":      func(t *testing.T, i *Input) { os.WriteFile(i.TurnKeyFile, []byte("SECRET INVALID PEM"), 0o600) },
		"mismatch":         func(t *testing.T, i *Input) { other := turnInput(t); i.TurnKeyFile = other.TurnKeyFile },
		"loose key":        func(t *testing.T, i *Input) { os.Chmod(i.TurnKeyFile, 0o644) },
		"directory":        func(t *testing.T, i *Input) { i.TurnCertFile = t.TempDir() },
		"oversized": func(t *testing.T, i *Input) {
			os.WriteFile(i.TurnCertFile, bytes.Repeat([]byte("x"), (1<<20)+1), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := turnInput(t)
			mutate(t, &in)
			output := filepath.Join(t.TempDir(), "matrix")
			_, err := Run(in, output)
			if err == nil {
				t.Fatal("accepted invalid TURN input")
			}
			if strings.Contains(err.Error(), "SECRET INVALID PEM") {
				t.Fatal("key leaked")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("output mutated before validation")
			}
		})
	}
}

func TestTurnTLSRenewalPreservesKeysAndInvalidRenewalPreservesOutputs(t *testing.T) {
	in := turnInput(t)
	dir := filepath.Join(t.TempDir(), "matrix")
	if _, err := Run(in, dir); err != nil {
		t.Fatal(err)
	}
	secrets := readAll(t, filepath.Join(dir, "secrets"))
	old, err := os.ReadFile(filepath.Join(dir, "livekit", "turn.crt"))
	if err != nil {
		t.Fatal(err)
	}
	writeTurnPair(t, in, in.TurnHost, time.Now().Add(2*time.Hour))
	// Support ACME's current-certificate symlinks.
	link := filepath.Join(t.TempDir(), "cert-link")
	if err := os.Symlink(in.TurnCertFile, link); err != nil {
		t.Fatal(err)
	}
	in.TurnCertFile = link
	if _, err := Run(in, dir); err != nil {
		t.Fatal(err)
	}
	fresh, _ := os.ReadFile(filepath.Join(dir, "livekit", "turn.crt"))
	if bytes.Equal(old, fresh) {
		t.Fatal("cert did not renew")
	}
	after := readAll(t, filepath.Join(dir, "secrets"))
	for k, v := range secrets {
		if v != after[k] {
			t.Fatalf("secret changed: %s", k)
		}
	}
	for _, name := range []string{"turn.crt", "turn.key"} {
		st, err := os.Stat(filepath.Join(dir, "livekit", name))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("private output: %v", err)
		}
	}
	config, _ := os.ReadFile(filepath.Join(dir, "livekit", "config.yaml"))
	if !strings.Contains(string(config), "tls_port: 5349") || !strings.Contains(string(config), "/config/turn.key") {
		t.Fatal("missing TLS config")
	}
	before := readAll(t, dir)
	os.WriteFile(in.TurnKeyFile, []byte("bad key"), 0o600)
	if _, err := Run(in, dir); err == nil {
		t.Fatal("invalid renewal accepted")
	}
	after = readAll(t, dir)
	for k, v := range before {
		if v != after[k] {
			t.Fatalf("invalid renewal changed: %s", k)
		}
	}
}

func TestTurnPeerPolicyAllowsOnlySFUAddress(t *testing.T) {
	denied := turnPeerDeny("192.0.2.10")
	for _, ip := range []string{"192.0.2.10", "192.0.2.9", "192.0.2.11", "8.8.8.8", "127.0.0.1", "192.168.1.1", "::1", "2001:db8::1"} {
		addr := netip.MustParseAddr(ip)
		blocked := false
		for _, prefix := range denied {
			if netip.MustParsePrefix(prefix).Contains(addr) {
				blocked = true
			}
		}
		if blocked == (ip == "192.0.2.10") {
			t.Fatalf("unexpected peer policy for %s", ip)
		}
	}
}
