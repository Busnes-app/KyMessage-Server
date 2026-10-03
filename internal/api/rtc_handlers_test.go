package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/matrixrtc"
)

func TestRTCCallGrantsAndRevocation(t *testing.T) {
	srv, _, cfg, mas := setupMatrixServer(t)
	cfg.Matrix.RTCHost = "https://sfu.example.com"
	rooms := newFakeRooms()
	api.SetRoomAdminForTest(srv, rooms)
	var removed []string
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatal("no authorization")
		}
		switch strings.TrimPrefix(r.URL.Path, "/twirp/livekit.RoomService/") {
		case "CreateRoom":
			w.Write([]byte(`{}`))
		case "ListRooms":
			json.NewEncoder(w).Encode(map[string]any{"rooms": []map[string]string{{"name": "call", "metadata": rGroup}}})
		case "ListParticipants":
			w.Write([]byte(`{"participants":[{"identity":"alice-device","metadata":"@alice:example.com"},{"identity":"bob-device","metadata":"@bob:example.com"}]}`))
		case "RemoveParticipant":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			removed = append(removed, in["identity"])
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "bad", 404)
		}
	}))
	defer live.Close()
	api.SetRTCForTest(srv, matrixrtc.New(live.URL, "key", strings.Repeat("s", 64)))
	user := "@alice:example.com"
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_matrix/federation/v1/openid/userinfo" || r.URL.Query().Get("access_token") != "openid-secret" {
			t.Error("wrong token verification", r.URL)
		}
		json.NewEncoder(w).Encode(map[string]string{"sub": user})
	}))
	defer verify.Close()
	api.SetHealthTargetsForTest(srv, health.Targets{Synapse: verify.URL})
	body := `{"room_id":"!grp:example.com","slot_id":"m.call#ROOM","openid_token":{"access_token":"openid-secret","matrix_server_name":"example.com"},"member":{"id":"membership","claimed_user_id":"@alice:example.com","claimed_device_id":"DEVICE"}}`
	request := func(body string, want int) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/matrix/rtc/get_token", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://chat.example.com")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("status %d want %d: %s", w.Code, want, w.Body)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Header())
		}
		return w
	}
	w := request(body, 200)
	var grant map[string]string
	json.Unmarshal(w.Body.Bytes(), &grant)
	if grant["url"] != "wss://sfu.example.com" {
		t.Fatal(grant)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(strings.Split(grant["jwt"], ".")[1])
	var token map[string]any
	json.Unmarshal(claims, &token)
	if token["sub"] != matrixrtc.Hash(user, "DEVICE", "membership") || token["video"].(map[string]any)["room"] != matrixrtc.Hash(rGroup, "m.call#ROOM") {
		t.Fatal(token)
	}
	request(strings.Replace(body, "example.com\"},\"member", "evil.example\"},\"member", 1), 403)
	request(body+`{}`, 400)
	user = "@bob:example.com"
	request(body, 403)
	user = "@alice:example.com"
	rooms.members[rGroup] = []string{"@bob:example.com"}
	request(body, 403)
	rooms.members[rGroup] = []string{"@alice:example.com", "@bob:example.com"}
	mas.usersErr = errors.New("MAS down")
	request(body, 502)
	mas.usersErr = nil
	if err := srv.ReconcileCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "bob-device" {
		t.Fatal("wrong eviction", removed)
	}
	removed = nil
	rooms.members[rGroup] = []string{"@bob:example.com"}
	if err := srv.ReconcileCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "alice-device,bob-device" {
		t.Fatal("removed room member survived", removed)
	}
}

func TestRTCPreflightUsesNoCookies(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	for _, path := range []string{"/api/matrix/rtc/get_token", "/api/matrix/rtc/sfu/get"} {
		req := httptest.NewRequest("OPTIONS", path, nil)
		req.Header.Set("Origin", "https://client.example")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal(w.Code, w.Header())
		}
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/api/matrix/rtc/get_token", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestRTCCallHealthReportsUpstreamFailure(t *testing.T) {
	srv, st, _, _ := setupMatrixServer(t)
	status := http.StatusOK
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/twirp/livekit.RoomService/ListRooms" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("incorrect LiveKit probe")
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"rooms":[]}`))
	}))
	defer live.Close()
	api.SetRTCForTest(srv, matrixrtc.New(live.URL, "key", strings.Repeat("s", 64)))
	admin := loginAs(t, srv, st, "root", "admin")
	for _, want := range []string{"up", "down"} {
		w := adminDo(t, srv, admin, "GET", "/api/admin/health", nil)
		found := false
		for _, component := range decode[componentsBody](t, w).Components {
			if component.Name == "livekit" {
				found = true
				if component.Status != want || component.Pinned != health.Pins["livekit"] || component.Version != "" {
					t.Fatalf("component: %+v", component)
				}
			}
		}
		if !found {
			t.Fatal("LiveKit missing from health")
		}
		status = http.StatusServiceUnavailable
	}
}
