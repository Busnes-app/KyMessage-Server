package matrixinit

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rsaPEM(t *testing.T, bits int, pkcs8 bool) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	if !pkcs8 {
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// A kept key below its floor is refused naming the file; the file stays byte for byte and no
// config is re-rendered with it. Strong kept keys (the controls) are kept and rendered.
func TestInitRefusesWeakKeptKeys(t *testing.T) {
	for name, tc := range map[string]struct {
		file string
		data []byte
		ok   bool
	}{
		"RSA-1024 PKCS#1":     {"mas_signing_rsa", rsaPEM(t, 1024, false), false},
		"RSA-2047 PKCS#8":     {"mas_signing_rsa", rsaPEM(t, 2047, true), false},
		"EC as RSA":           {"mas_signing_rsa", []byte(mustEC(t)), false},
		"RSA-2048 PKCS#8":     {"mas_signing_rsa", rsaPEM(t, 2048, true), true},
		"RSA-3072 PKCS#1":     {"mas_signing_rsa", rsaPEM(t, 3072, false), true},
		"31-byte LiveKit key": {"livekit_api_secret", []byte(strings.Repeat("a", 31)), false},
		"32-byte LiveKit key": {"livekit_api_secret", []byte(strings.Repeat("a", 32)), true},
		"short macaroon key":  {"synapse_macaroon_secret_key", []byte("0123456789abcdef"), false},
		"short form secret":   {"synapse_form_secret", []byte("0123456789abcdef"), false},
		"31-byte MAS AES key": {"mas_encryption", []byte(strings.Repeat("ab", 31)), false},
		"non-hex MAS AES key": {"mas_encryption", []byte(strings.Repeat("zz", 32)), false},
		"32-byte MAS AES key": {"mas_encryption", []byte(strings.Repeat("ab", 32)), true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "m")
			if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "secrets", tc.file)
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			masCfg, _ := os.ReadFile(filepath.Join(dir, "mas", "config.yaml"))
			_, err := Run(goodInput(), dir)
			if kept, _ := os.ReadFile(path); !bytes.Equal(kept, tc.data) {
				t.Fatal("the kept key was changed")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "mas", "config.yaml"))
			if tc.ok {
				if err != nil {
					t.Fatalf("strong key refused: %v", err)
				}
				if tc.file == "mas_signing_rsa" && !bytes.Contains(after, []byte(quote(string(tc.data)))) {
					t.Fatal("strong kept key not rendered")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "secrets/"+tc.file) {
				t.Fatalf("err %v, want a refusal naming the file", err)
			}
			if !bytes.Equal(after, masCfg) {
				t.Fatal("mas/config.yaml rewritten after a refusal")
			}
		})
	}
}

func mustEC(t *testing.T) string {
	t.Helper()
	b, err := ecKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeTurnChain writes a leaf for host signed by an issuer whose certificate follows it in the
// file, as ACME fullchain files do. Each key is RSA of the given size, or P-256 when 0.
func writeTurnChain(t *testing.T, in Input, leafBits, issuerBits int) {
	t.Helper()
	newKey := func(bits int) crypto.Signer {
		var k crypto.Signer
		var err error
		if bits == 0 {
			k, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		} else {
			k, err = rsa.GenerateKey(rand.Reader, bits)
		}
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	issuerKey, leafKey := newKey(issuerBits), newKey(leafBits)
	now := time.Now()
	issuer := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	issuerDER, err := x509.CreateCertificate(rand.Reader, issuer, issuer, issuerKey.Public(), issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	issuer, _ = x509.ParseCertificate(issuerDER)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: in.TurnHost}, DNSNames: []string{in.TurnHost}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, issuer, leafKey.Public(), issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuerDER})...)
	if err := os.WriteFile(in.TurnCertFile, chain, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.TurnKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every certificate in the supplied TURN chain needs RSA-2048 or an EC key; a weak leaf or
// intermediate is refused before anything is written.
func TestTurnTLSRefusesWeakChainKeys(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		leafBits, issuerBits int
		ok                   bool
	}{
		{"RSA-1024 leaf", 1024, 2048, false},
		{"RSA-2047 leaf", 2047, 2048, false},
		{"RSA-1024 intermediate", 0, 1024, false},
		{"RSA-2047 intermediate", 2048, 2047, false},
		{"RSA-2048 chain", 2048, 2048, true},
		{"P-256 leaf under RSA-2048", 0, 2048, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := turnInput(t)
			writeTurnChain(t, in, tc.leafBits, tc.issuerBits)
			output := filepath.Join(t.TempDir(), "matrix")
			_, err := Run(in, output)
			if tc.ok {
				if err != nil {
					t.Fatalf("strong chain refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "2048") {
				t.Fatalf("err %v, want a key-size refusal", err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("output mutated before validation")
			}
		})
	}
}
