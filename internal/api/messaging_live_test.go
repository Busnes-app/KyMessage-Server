package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

type liveNotice struct {
	Kind       string `json:"kind"`
	Sequence   int64  `json:"sequence"`
	Epoch      int64  `json:"epoch"`
	RosterHash string `json:"roster_hash"`
}

func readLive(t *testing.T, c *websocket.Conn) liveNotice {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var notice liveNotice
	if err := wsjson.Read(ctx, c, &notice); err != nil {
		t.Fatal(err)
	}
	if notice.Kind != "wake" || len(notice.RosterHash) != 64 {
		t.Fatal(notice)
	}
	return notice
}
func TestMessagingLiveWakeReconnectAndRevocation(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	session := messagingLogin(t, st, "live-alice")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Live"})
	messagingCode(t, w, 201)
	var room struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(srv)
	t.Cleanup(func() { srv.StopMessaging(); srv.WaitDetached(); server.Close() })
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/messaging/rooms/" + room.ID + "/live"
	dial := func() *websocket.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + session}, "Origin": {cfg.Server.AppURL}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		if err := c.Write(ctx, websocket.MessageText, []byte(d.Token)); err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := dial()
	initial := readLive(t, c)
	body := map[string]any{"id": uuid.NewString(), "kind": "commit", "epoch": 0, "roster_hash": initial.RosterHash, "payload": "b3BhcXVl"}
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/rooms/"+room.ID+"/events", session, d.Token, body), 200)
	notice := readLive(t, c)
	if notice.Sequence != 1 || notice.Epoch != 1 {
		t.Fatal(notice)
	}
	c.CloseNow()
	c = dial()
	if got := readLive(t, c); got.Sequence != 1 {
		t.Fatal("reconnect failed", got)
	}
	messagingCode(t, messagingRequest(t, srv, "DELETE", "/api/messaging/devices/"+d.ID, session, "", nil), 200)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("revoked connection did not close promptly", err)
	}
}

func TestMessagingLiveBoundariesAndShutdown(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	session := messagingLogin(t, st, "live-owner")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	other := messagingLogin(t, st, "live-other")
	od := verifyEnrollment(t, srv, other, requestEnrollment(t, srv, other))
	w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Live"})
	var room struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(srv)
	t.Cleanup(func() { srv.StopMessaging(); srv.WaitDetached(); server.Close() })
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/messaging/rooms/" + room.ID + "/live"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tc := range []struct {
		origin, session, suffix string
		status                  int
	}{
		{"", session, "", 403}, {"https://attacker.invalid", session, "", 403}, {cfg.Server.AppURL + "/path", session, "", 403},
		{cfg.Server.AppURL, "", "", 401}, {cfg.Server.AppURL, session, "?token=forbidden", 400},
	} {
		c, response, err := websocket.Dial(ctx, endpoint+tc.suffix, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {tc.origin}, "Authorization": {"Bearer " + tc.session}}})
		if c != nil {
			c.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != tc.status {
			t.Fatalf("handshake status %v, %v", response, err)
		}
	}
	for _, tc := range []struct {
		session, credential string
		kind                websocket.MessageType
	}{
		{session, od.Token, websocket.MessageText}, {other, od.Token, websocket.MessageText},
		{session, "not-hex", websocket.MessageText}, {session, strings.Repeat("a", 65), websocket.MessageText},
		{session, d.Token, websocket.MessageBinary},
	} {
		c, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {cfg.Server.AppURL}, "Authorization": {"Bearer " + tc.session}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Write(ctx, tc.kind, []byte(tc.credential)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := c.Read(ctx); err == nil || ctx.Err() != nil {
			t.Fatal("invalid connection returned a notice", err)
		}
		c.CloseNow()
	}
	connections := []*websocket.Conn{}
	for range 4 {
		c, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {cfg.Server.AppURL}, "Authorization": {"Bearer " + session}}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.CloseNow()
		if err := c.Write(ctx, websocket.MessageText, []byte(d.Token)); err != nil {
			t.Fatal(err)
		}
		readLive(t, c)
		connections = append(connections, c)
	}
	if c, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {cfg.Server.AppURL}, "Authorization": {"Bearer " + session}}}); err == nil || response.StatusCode != 503 {
		if c != nil {
			c.CloseNow()
		}
		t.Fatal("connection quota", err)
	}
	srv.StopMessaging()
	done := make(chan struct{})
	go func() { srv.WaitDetached(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("shutdown did not drain")
	}
	for _, c := range connections {
		if _, _, err := c.Read(ctx); err == nil {
			t.Fatal("stream survived shutdown")
		}
	}
}

func TestMessagingLiveSessionRevocationHeartbeat(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	session := messagingLogin(t, st, "live-session")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Session"})
	var room struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(srv)
	t.Cleanup(func() { srv.StopMessaging(); srv.WaitDetached(); server.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, fmt.Sprintf("ws%s/api/messaging/rooms/%s/live", strings.TrimPrefix(server.URL, "http"), room.ID), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {cfg.Server.AppURL}, "Authorization": {"Bearer " + session}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte(d.Token)); err != nil {
		t.Fatal(err)
	}
	readLive(t, c)
	if err := st.Sessions().DeleteSession(ctx, crypto.SHA256Hex([]byte(session))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("revoked session stream survived heartbeat", err)
	}
}
