package matrixrtc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMSC4195HashVector(t *testing.T) {
	if got := Hash("!roomid:example.com", "slot1234"); got != "O8437W3+jmzMVjoIP3tNwbm+XxHQk2iKpOA7aqw3qSc" {
		t.Fatal(got)
	}
}

func TestJoinTokenScopeAndSignature(t *testing.T) {
	c := New("http://unused", "key", strings.Repeat("x", 64))
	tok := c.Join("room", "identity", "@alice:example.com")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatal(tok)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(c.secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		t.Fatal("bad signature")
	}
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	json.Unmarshal(b, &claims)
	grant := claims["video"].(map[string]any)
	if claims["sub"] != "identity" || claims["metadata"] != "@alice:example.com" || grant["room"] != "room" || grant["roomJoin"] != true || grant["canUpdateOwnMetadata"] != false || grant["roomAdmin"] != nil || grant["roomCreate"] != nil {
		t.Fatal(claims)
	}
	ttl := int64(claims["exp"].(float64)) - time.Now().Unix()
	if ttl < 14 || ttl > 15 {
		t.Fatal("TTL", ttl)
	}
}

func TestLiveKitDoesNotFollowRedirects(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("leaked token to redirect") }))
	defer foreign.Close()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, foreign.URL, 307) }))
	defer hs.Close()
	if err := New(hs.URL, "key", "secret").Create(context.Background(), "room", "!room"); err == nil {
		t.Fatal("redirect accepted")
	}
}
