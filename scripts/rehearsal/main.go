//go:build rehearsal

// Test-only helper for scripts/restore-messages-rehearsal.sh; the tag keeps it out of
// every production build. It touches only the scratch paths and loopback URL it is given.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/google/uuid"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: rehearsal keygen|session|pin|seed [flags]")
	}
	cmds := map[string]func([]string) error{"keygen": keygen, "session": session, "pin": pin, "seed": seed}
	run, ok := cmds[os.Args[1]]
	if !ok {
		log.Fatalf("unknown command %q", os.Args[1])
	}
	if err := run(os.Args[2:]); err != nil {
		log.Fatalf("%s: %v", os.Args[1], err)
	}
}

// scratch refuses a path outside os.TempDir(), so a mistyped flag cannot write to a real
// instance or home directory.
func scratch(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	tmp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(tmp, abs); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is not under %s", path, tmp)
	}
	return abs, nil
}

// keygen makes a throwaway 2-of-3 recovery key the way the restore tests do and keeps
// two shares in a 0600 file; the private key itself is never written.
func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "scratch directory")
	_ = fs.Parse(args)
	dir, err := scratch(*out)
	if err != nil {
		return err
	}
	priv, err := recoverykey.Generate()
	if err != nil {
		return err
	}
	shares, err := recoverykey.Split(priv, 2, 3)
	if err != nil {
		return err
	}
	pub := base64.StdEncoding.EncodeToString(priv.Public().Bytes())
	if err := os.WriteFile(filepath.Join(dir, "public.b64"), []byte(pub), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "shares"), []byte(shares[0].String()+"\n"+shares[2].String()+"\n"), 0o600)
}

// session stands in for a suite OIDC sign-in, as the mls-proof fixture does: it creates
// the suite account if absent and prints a fresh session token.
func session(args []string) error {
	fs := flag.NewFlagSet("session", flag.ExitOnError)
	data := fs.String("data", "", "KY_DATA_DIR of a scratch instance")
	user := fs.String("user", "", "suite account ID")
	_ = fs.Parse(args)
	dir, err := scratch(*data)
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(dir, "ky_server.db"), DataDir: dir})
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Users().GetUserByID(ctx, *user); errors.Is(err, store.ErrNotFound) {
		err = st.Users().CreateUser(ctx, &store.User{ID: *user, Username: *user, Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: *user})
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	token := crypto.RandomHex(32)
	now := time.Now().UTC()
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: crypto.SHA256Hex([]byte(token)), UserID: *user, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, ""); err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}

type client struct {
	http          *http.Client
	origin        string
	bearer, token string
}

func newClient(origin string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{http: &http.Client{Jar: jar, Timeout: 15 * time.Second}, origin: origin}
}

// do sends JSON and decodes the response into out, failing on any status but want.
func (c *client) do(method, path string, body, out any, want int) error {
	var raw io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		raw = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.origin+path, raw)
	if err != nil {
		return err
	}
	req.Header.Set("Origin", c.origin)
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range c.http.Jar.Cookies(req.URL) {
		if ck.Name == "ky_csrf" {
			req.Header.Set("X-CSRF-Token", ck.Value)
		}
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.token != "" {
		req.Header.Set("X-KyMessages-Device", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

// pin replaces the bootstrap password, then pins the throwaway public key by hand.
func pin(args []string) error {
	fs := flag.NewFlagSet("pin", flag.ExitOnError)
	origin := fs.String("url", "", "loopback server origin")
	dir := fs.String("dir", "", "scratch directory holding public.b64")
	_ = fs.Parse(args)
	bootstrap, replacement := os.Getenv("REHEARSAL_ADMIN_PASSWORD"), crypto.RandomHex(24)
	pub, err := os.ReadFile(filepath.Join(*dir, "public.b64"))
	if err != nil {
		return err
	}
	c := newClient(*origin)
	login := func(secret string) error {
		return c.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": secret}, nil, 200)
	}
	if err := login(bootstrap); err != nil {
		return err
	}
	if err := c.do("POST", "/api/auth/change-password", map[string]string{"current_password": bootstrap, "new_password": replacement}, nil, 200); err != nil {
		return err
	}
	if err := login(replacement); err != nil {
		return err
	}
	return c.do("POST", "/api/backup/pin-key", map[string]any{"public_key": string(pub), "threshold": 2, "total_shares": 3}, nil, 200)
}

// seed enrolls one Ed25519 device, creates a room, initializes epoch 1 and appends one
// application event. The payloads are opaque test bytes; the server never parses MLS.
func seed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	origin := fs.String("url", "", "loopback server origin")
	bearer := fs.String("session", "", "suite session token")
	out := fs.String("dir", "", "scratch directory for the device token and room ID")
	_ = fs.Parse(args)
	dir, err := scratch(*out)
	if err != nil {
		return err
	}
	c := newClient(*origin)
	c.bearer = *bearer

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	var enrolled struct {
		Device       struct{ ID string } `json:"device"`
		SigningInput string              `json:"signing_input"`
	}
	if err := c.do("POST", "/api/messaging/devices", map[string]string{"name": "rehearsal", "public_key": base64.StdEncoding.EncodeToString(pub), "token_hash": crypto.SHA256Hex([]byte(token))}, &enrolled, 201); err != nil {
		return err
	}
	input, err := base64.StdEncoding.DecodeString(enrolled.SigningInput)
	if err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, input))
	if err := c.do("POST", "/api/messaging/devices/"+enrolled.Device.ID+"/verify", map[string]string{"signature": sig}, nil, 200); err != nil {
		return err
	}
	c.token = token

	var room struct{ ID string }
	if err := c.do("POST", "/api/messaging/rooms", map[string]string{"name": "rehearsal room"}, &room, 201); err != nil {
		return err
	}
	appendEvent := func(kind string) error {
		var state struct {
			Epoch      int64  `json:"epoch"`
			RosterHash string `json:"roster_hash"`
		}
		if err := c.do("GET", "/api/messaging/rooms/"+room.ID+"/delivery", nil, &state, 200); err != nil {
			return err
		}
		body := map[string]any{"id": uuid.NewString(), "kind": kind, "epoch": state.Epoch, "roster_hash": state.RosterHash,
			"payload": base64.StdEncoding.EncodeToString([]byte("rehearsal " + kind)), "welcomes": map[string]string{}}
		return c.do("POST", "/api/messaging/rooms/"+room.ID+"/events", body, nil, 200)
	}
	if err := appendEvent("commit"); err != nil {
		return err
	}
	if err := appendEvent("application"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "device-token"), []byte(token), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "room-id"), []byte(room.ID), 0o600)
}
