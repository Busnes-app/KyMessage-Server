package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

// fakeRooms stands in for Synapse's admin API. Task 3 grows it.
type fakeRooms struct {
	mu     sync.Mutex
	rooms  []synapseadmin.Room
	err    error
	tokens []string
}

func newFakeRooms() *fakeRooms { return &fakeRooms{} }

func (f *fakeRooms) Rooms(_ context.Context, tok string, q synapseadmin.RoomQuery) (synapseadmin.RoomPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, tok)
	if f.err != nil {
		return synapseadmin.RoomPage{}, f.err
	}
	n := len(f.rooms)
	return synapseadmin.RoomPage{Rooms: f.rooms[min(q.Offset, n):min(q.Offset+q.Limit, n)], Total: n}, nil
}

// setupRoomsServer is setupMatrixServer with its Synapse admin fake in hand.
func setupRoomsServer(t *testing.T) (*api.Server, store.Store, *fakeMAS, *fakeRooms) {
	t.Helper()
	srv, st, _, f := setupMatrixServer(t)
	rooms := newFakeRooms()
	api.SetRoomAdminForTest(srv, rooms)
	return srv, st, f, rooms
}

// synapseAdminHealth is the synapse-admin component of one Health request.
func synapseAdminHealth(t *testing.T, srv *api.Server, admin *http.Cookie) health.Component {
	t.Helper()
	h := decode[componentsBody](t, adminDo(t, srv, admin, "GET", "/api/admin/health", nil))
	for _, c := range h.Components {
		if c.Name == "synapse-admin" {
			return c
		}
	}
	t.Fatalf("no synapse-admin in %+v", h.Components)
	return health.Component{}
}

func TestHealthProvesSynapseAdminAccess(t *testing.T) {
	srv, st, f, rooms := setupRoomsServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	get := func() health.Component { return synapseAdminHealth(t, srv, admin) }
	if c := get(); c.Status != "up" || c.Error != "" || len(rooms.tokens) != 1 || rooms.tokens[0] != "console-token" || f.consoleRuns != 1 {
		t.Fatalf("working: %+v tokens %v runs %d", c, rooms.tokens, f.consoleRuns)
	}
	rooms.err = &synapseadmin.Error{Method: "GET", Path: "/_synapse/admin/v1/rooms", Status: 401, Errcode: "M_UNKNOWN_TOKEN", Message: "Token is not active"}
	if c := get(); c.Status != "down" || !strings.Contains(c.Error, "Token is not active") {
		t.Errorf("token refused: %+v", c)
	}
	f.consoleErr = errors.New("console account is locked in MAS")
	if c := get(); c.Status != "down" || c.Error != "console account is locked in MAS" {
		t.Errorf("locked: %+v", c)
	}
}

// A MAS that never answers (and ignores the context) cannot hold Health past its deadline.
func TestHealthSynapseAdminProbeCannotHang(t *testing.T) {
	srv, st, f, _ := setupRoomsServer(t)
	f.consoleHang = make(chan struct{})
	t.Cleanup(func() { close(f.consoleHang) })
	admin := loginAs(t, srv, st, "root", "admin")
	start := time.Now()
	c := synapseAdminHealth(t, srv, admin)
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("health took %s", d)
	}
	if c.Status != "down" || !strings.Contains(c.Error, "timed out") {
		t.Fatalf("%+v", c)
	}
}
