package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/matrixrtc"
)

// fakeLiveKit serves rooms (name -> Matrix room ID metadata) and their participants
// (identity -> MXID), records removals, and fails ListParticipants for rooms in failing.
type fakeLiveKit struct {
	mu           sync.Mutex
	rooms        [][2]string
	participants map[string][][2]string
	failing      map[string]bool
	removed      []string
}

func (f *fakeLiveKit) serve(t *testing.T, srv *api.Server) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		switch strings.TrimPrefix(r.URL.Path, "/twirp/livekit.RoomService/") {
		case "ListRooms":
			var out []map[string]string
			for _, r := range f.rooms {
				out = append(out, map[string]string{"name": r[0], "metadata": r[1]})
			}
			json.NewEncoder(w).Encode(map[string]any{"rooms": out})
		case "ListParticipants":
			if f.failing[in["room"]] {
				http.Error(w, `{"code":"internal","msg":"boom"}`, 500)
				return
			}
			var out []map[string]string
			for _, p := range f.participants[in["room"]] {
				out = append(out, map[string]string{"identity": p[0], "metadata": p[1]})
			}
			json.NewEncoder(w).Encode(map[string]any{"participants": out})
		case "RemoveParticipant":
			f.removed = append(f.removed, in["room"]+"/"+in["identity"])
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected LiveKit call %s", r.URL.Path)
			http.Error(w, "bad", 404)
		}
	}))
	t.Cleanup(live.Close)
	api.SetRTCForTest(srv, matrixrtc.New(live.URL, "key", strings.Repeat("s", 64)))
}

func (f *fakeLiveKit) takeRemoved() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.removed
	f.removed = nil
	slices.Sort(out)
	return out
}

// MAS down: no local user can be confirmed, so none stays (fail closed), and a server no
// longer allowed loses its users too; an allowed remote user stays.
func TestReconcileCallsEvictsUnverifiedUsersWhenMASIsDown(t *testing.T) {
	srv, st, cfg, mas := setupMatrixServer(t)
	cfg.Matrix.RTCHost = "https://sfu.example.com"
	rooms := newFakeRooms()
	rooms.members[rGroup] = []string{"@alice:example.com", "@bob:peer.example", "@eve:gone.example"}
	api.SetRoomAdminForTest(srv, rooms)
	for k, v := range map[string]string{"federation_level": "manual", "federation_servers": `["peer.example"]`} {
		if err := st.Settings().SetSetting(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	lk := &fakeLiveKit{
		rooms: [][2]string{{"call", rGroup}},
		participants: map[string][][2]string{"call": {
			{"alice-device", "@alice:example.com"}, {"bob-device", "@bob:peer.example"}, {"eve-device", "@eve:gone.example"},
		}},
	}
	lk.serve(t, srv)

	if err := srv.ReconcileCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := lk.takeRemoved(); strings.Join(got, ",") != "call/eve-device" {
		t.Fatalf("with MAS up evicted %v, want only the disallowed server's user", got)
	}

	mas.usersErr = errors.New("MAS down")
	mas.consoleErr = errors.New("MAS down")
	if err := srv.ReconcileCalls(context.Background()); err == nil {
		t.Fatal("MAS failure not reported")
	}
	if got := lk.takeRemoved(); strings.Join(got, ",") != "call/alice-device,call/eve-device" {
		t.Fatalf("with MAS down evicted %v, want the unverified local user and the disallowed remote one", got)
	}
}

// One room failing (LiveKit or Synapse) is reported and skipped; the rooms around it are
// still reconciled, all through a single console session. Alice is active but has left every
// room, so each readable room's call drops her.
func TestReconcileCallsSurvivesAFailingRoom(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(lk *fakeLiveKit, rooms *fakeRooms)
		want string
	}{
		{"participants", func(lk *fakeLiveKit, _ *fakeRooms) { lk.failing = map[string]bool{"c2": true} },
			"c1/alice-device,c3/alice-device"},
		{"membership", func(_ *fakeLiveKit, rooms *fakeRooms) {
			rooms.roomErr = map[string]error{"!r2:example.com": errors.New("synapse 500")}
		}, "c1/alice-device,c3/alice-device"},
		// Unverifiable metadata: the call keeps nobody.
		{"metadata", func(lk *fakeLiveKit, _ *fakeRooms) { lk.rooms[1][1] = "not a room" },
			"c1/alice-device,c2/alice-device,c3/alice-device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, cfg, mas := setupMatrixServer(t)
			cfg.Matrix.RTCHost = "https://sfu.example.com"
			rooms := newFakeRooms()
			lk := &fakeLiveKit{participants: map[string][][2]string{}}
			for _, n := range []string{"1", "2", "3"} {
				id := "!r" + n + ":example.com"
				rooms.rooms[id] = rooms.rooms[rGroup]
				rooms.members[id] = []string{"@bob:example.com"}
				lk.rooms = append(lk.rooms, [2]string{"c" + n, id})
				lk.participants["c"+n] = [][2]string{{"alice-device", "@alice:example.com"}}
			}
			api.SetRoomAdminForTest(srv, rooms)
			lk.serve(t, srv)
			tc.fail(lk, rooms)

			if err := srv.ReconcileCalls(context.Background()); err == nil {
				t.Fatal("the failing room was not reported")
			}
			if got := strings.Join(lk.takeRemoved(), ","); got != tc.want {
				t.Fatalf("evicted %s, want %s", got, tc.want)
			}
			if mas.consoleRuns != 1 {
				t.Fatalf("%d console sessions for one run, want 1", mas.consoleRuns)
			}
		})
	}
}
