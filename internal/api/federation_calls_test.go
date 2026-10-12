package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/matrixrtc"
)

func TestRemoteCallersJoinOnlyAllowedExistingCalls(t *testing.T) {
	srv, st, cfg, _ := setupMatrixServer(t)
	cfg.Matrix.RTCHost = "https://sfu.example.com"
	rooms := newFakeRooms()
	rooms.members[rGroup] = []string{"@alice:example.com", "@bob:peer.example"}
	api.SetRoomAdminForTest(srv, rooms)
	callName := matrixrtc.Hash(rGroup, "m.call#ROOM")

	var mu sync.Mutex
	var liveRooms []map[string]string
	var created, removed []string
	participants := `{"participants":[{"identity":"alice-device","metadata":"@alice:example.com"},{"identity":"bob-device","metadata":"@bob:peer.example"}]}`
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch strings.TrimPrefix(r.URL.Path, "/twirp/livekit.RoomService/") {
		case "CreateRoom":
			created = append(created, "x")
			w.Write([]byte(`{}`))
		case "ListRooms":
			json.NewEncoder(w).Encode(map[string]any{"rooms": liveRooms})
		case "ListParticipants":
			w.Write([]byte(participants))
		case "RemoveParticipant":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			removed = append(removed, in["identity"])
			w.Write([]byte(`{}`))
		default:
			http.Error(w, "bad", 404)
		}
	}))
	defer live.Close()
	api.SetRTCForTest(srv, matrixrtc.New(live.URL, "key", strings.Repeat("s", 64)))
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a remote token reached our Synapse: %s", r.URL)
	}))
	defer local.Close()
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: local.URL})

	remoteSub, peerHits := "@bob:peer.example", 0
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		peerHits++
		sub := remoteSub
		mu.Unlock()
		if r.URL.Path != "/_matrix/federation/v1/openid/userinfo" || r.URL.Query().Get("access_token") != "peer-secret" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"sub": sub})
	}))
	defer peer.Close()
	api.SetFederationPeerForTest(srv, peer.URL, peer.Client())

	body := `{"room_id":"` + rGroup + `","slot_id":"m.call#ROOM","openid_token":{"access_token":"peer-secret","matrix_server_name":"peer.example"},"member":{"id":"m","claimed_user_id":"@bob:peer.example","claimed_device_id":"BOBDEV"}}`
	request := func(want int) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/matrix/rtc/get_token", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("status %d want %d: %s", w.Code, want, w.Body)
		}
	}
	set := func(key, value string) {
		t.Helper()
		if err := st.Settings().SetSetting(context.Background(), key, value); err != nil {
			t.Fatal(err)
		}
	}

	request(403) // federation closed: the peer is never contacted
	set("federation_servers", `["peer.example"]`)
	request(403) // listed but closed
	if peerHits != 0 {
		t.Fatalf("peer contacted %d times while not allowed", peerHits)
	}
	set("federation_level", "manual")
	request(403) // no call yet: remote users never start one
	if len(created) != 0 {
		t.Fatal("a remote user created a LiveKit room")
	}
	liveRooms = []map[string]string{{"name": callName, "metadata": rGroup}}
	request(200)
	remoteSub = "@bob:evil.example"
	request(401) // a token for another server's user
	remoteSub = "@bob:peer.example"
	rooms.members[rGroup] = []string{"@alice:example.com"}
	request(403) // not in the room
	rooms.members[rGroup] = []string{"@alice:example.com", "@bob:peer.example"}

	if err := srv.ReconcileCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("allowed participants evicted: %v", removed)
	}
	// A closed room keeps its remote members in Synapse, but grants no call and its call keeps
	// nobody.
	rooms.blocked[rGroup] = true
	request(403)
	if err := srv.ReconcileCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "alice-device,bob-device" {
		t.Fatalf("closed room evicted %v, want everyone", removed)
	}
	removed = nil
	rooms.blocked[rGroup] = false

	// Unreadable federation settings evict remote callers and still evict local ones.
	set("federation_servers", "not json")
	if err := srv.ReconcileCalls(context.Background()); err == nil {
		t.Error("a settings error was not reported")
	}
	if strings.Join(removed, ",") != "bob-device" {
		t.Fatalf("with unreadable settings evicted %v, want the remote caller", removed)
	}
	removed = nil
	set("federation_servers", `["peer.example"]`)
	set("federation_level", "closed") // takes effect on calls at once, without a Synapse restart
	if err := srv.ReconcileCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "bob-device" {
		t.Fatalf("evicted %v, want only the remote caller", removed)
	}
}

func TestFederationClientRefusesNonPublicAddresses(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "10.1.2.3:8448", "172.20.0.5:443", "192.168.1.1:443", "100.64.0.1:443",
		"169.254.169.254:80", "[::1]:443", "[fd00::1]:443", "[fe80::1]:443", "[::ffff:127.0.0.1]:443", "0.0.0.0:443"} {
		if err := api.PublicOnlyForTest("tcp", addr, nil); err == nil {
			t.Errorf("%s allowed", addr)
		}
	}
	for _, addr := range []string{"93.184.215.14:443", "[2606:4700::1111]:8448"} {
		if err := api.PublicOnlyForTest("tcp", addr, nil); err != nil {
			t.Errorf("%s refused: %v", addr, err)
		}
	}
	srv, _, _ := setupTestServer(t)
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the federation client reached a loopback server")
	}))
	defer internal.Close()
	if _, err := api.FederationHTTPForTest(srv).Get(internal.URL); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Errorf("loopback dial: %v", err)
	}
}

func TestFederationBaseFollowsWellKnown(t *testing.T) {
	answer := ""
	wk := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/www/.well-known/matrix/server":
			w.Write([]byte(`{"m.server":"matrix.peer.example:443"}`))
		case r.URL.Path != "/.well-known/matrix/server" || answer == "":
			http.NotFound(w, r)
		case answer == "redirect":
			http.Redirect(w, r, "https://www.peer.example/www/.well-known/matrix/server", http.StatusMovedPermanently)
		case answer == "redirect-http":
			http.Redirect(w, r, "http://www.peer.example/www/.well-known/matrix/server", http.StatusMovedPermanently)
		default:
			w.Write([]byte(answer))
		}
	}))
	defer wk.Close()
	// Every host dials the test server; the TLS check is the test server's own.
	client := wk.Client()
	tr := client.Transport.(*http.Transport).Clone()
	plainHit := false
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHit = true
		w.Write([]byte(`{"m.server":"evil.example:443"}`))
	}))
	defer plain.Close()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		target := wk.Listener.Addr().String()
		if strings.HasSuffix(addr, ":80") {
			target = plain.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	tr.TLSClientConfig.InsecureSkipVerify = true // the certificate names 127.0.0.1, not peer.example
	client.Transport = tr
	srv, _, _ := setupTestServer(t)
	for _, c := range []struct{ answer, want string }{
		{"", "https://peer.example:8448"},
		{`{"m.server":"matrix.peer.example"}`, "https://matrix.peer.example:8448"},
		{`{"m.server":"matrix.peer.example:443"}`, "https://matrix.peer.example:443"},
		{`{"m.server":""}`, "https://peer.example:8448"},
	} {
		answer = c.answer
		if got, err := api.FederationBaseForTest(srv, client, "peer.example"); err != nil || got != c.want {
			t.Errorf("%q: %q, %v; want %q", c.answer, got, err, c.want)
		}
	}
	// A redirect is followed (mozilla.org answers 301 to www.mozilla.org), https only.
	answer = "redirect"
	if got, err := api.FederationBaseForTest(srv, client, "peer.example"); err != nil || got != "https://matrix.peer.example:443" {
		t.Errorf("redirected well-known: %q, %v", got, err)
	}
	answer = "redirect-http"
	if got, err := api.FederationBaseForTest(srv, client, "peer.example"); err != nil || got != "https://peer.example:8448" {
		t.Errorf("an https-to-http redirect was followed: %q, %v", got, err)
	}
	if plainHit {
		t.Error("the client fetched an http:// redirect target")
	}
	for _, bad := range []string{`{"m.server":"evil.example/path"}`, `{"m.server":"user@evil.example"}`, `{"m.server":":443"}`} {
		answer = bad
		if got, err := api.FederationBaseForTest(srv, client, "peer.example"); err == nil {
			t.Errorf("%s accepted as %q", bad, got)
		}
	}
}
