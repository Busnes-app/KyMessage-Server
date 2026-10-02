package synapseadmin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const grp = "!grp:example.com"

// synapseFake knows one room, !grp:example.com, and records changes.
type synapseFake struct {
	mu      sync.Mutex
	bodies  []string
	deleted []string
}

func newSynapseFake(t *testing.T) (*synapseFake, *Client) {
	t.Helper()
	f := &synapseFake{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, New(srv.URL)
}

func (f *synapseFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"Token is not active"}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	notFound := func(msg string) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"errcode":"M_NOT_FOUND","error":%q}`, msg)
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/rooms/"+grp:
		fmt.Fprint(w, `{"room_id":"!grp:example.com","name":"Team chat","canonical_alias":null,"creator":"@alice:example.com","joined_members":2,"joined_local_members":2,"encryption":"m.megolm.v1.aes-sha2","public":false,"join_rules":"invite","state_events":12,"topic":"plans","avatar":null}`)
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/rooms/"+grp+"/members":
		fmt.Fprint(w, `{"members":["@alice:example.com","@bob:example.com"],"total":2}`)
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/rooms/"+grp+"/block":
		fmt.Fprint(w, `{"block":true,"user_id":"@kymessages-console:example.com"}`)
	case r.Method == http.MethodDelete && p == "/_synapse/admin/v2/rooms/"+grp:
		f.bodies = append(f.bodies, string(body))
		fmt.Fprintf(w, `{"delete_id":"D%d"}`, len(f.bodies))
	case r.Method == http.MethodGet && p == "/_synapse/admin/v2/rooms/"+grp+"/delete_status":
		fmt.Fprint(w, `{"results":[
			{"delete_id":"D1","room_id":"!grp:example.com","status":"complete","shutdown_room":{"kicked_users":["@bob:example.com"],"failed_to_kick_users":[],"local_aliases":[],"new_room_id":null}},
			{"delete_id":"D2","room_id":"!grp:example.com","status":"active","shutdown_room":null}]}`)
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/room/"+grp+"/media":
		fmt.Fprint(w, `{"local":["mxc://example.com/AVATAR1","mxc://example.com/Pic_2-b"],"remote":["mxc://elsewhere.org/X"]}`)
	case r.Method == http.MethodGet && p == "/_synapse/admin/v1/room/!odd:example.com/media":
		fmt.Fprint(w, `{"local":["https://evil.example/x"],"remote":[]}`)
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/_synapse/admin/v1/media/example.com/"):
		id := strings.TrimPrefix(p, "/_synapse/admin/v1/media/example.com/")
		if id == "GONE" {
			notFound("Unknown media")
			return
		}
		f.deleted = append(f.deleted, id)
		fmt.Fprintf(w, `{"deleted_media":[%q],"total":1}`, id)
	case strings.Contains(p, "/delete_status"):
		notFound("No delete task for room_id found")
	default:
		notFound("Room not found")
	}
}

func TestRoomDetailMembersAndBlock(t *testing.T) {
	_, c := newSynapseFake(t)
	ctx := context.Background()
	r, err := c.Room(ctx, "tok", grp)
	want := Room{ID: grp, Name: "Team chat", Creator: "@alice:example.com", Members: 2, LocalMembers: 2,
		Encryption: "m.megolm.v1.aes-sha2", JoinRule: "invite", StateEvents: 12}
	if err != nil || r != want {
		t.Fatalf("room %+v %v", r, err)
	}
	m, err := c.Members(ctx, "tok", grp)
	if err != nil || !reflect.DeepEqual(m, []string{"@alice:example.com", "@bob:example.com"}) {
		t.Fatalf("members %v %v", m, err)
	}
	if b, err := c.Blocked(ctx, "tok", grp); err != nil || !b {
		t.Fatalf("blocked %v %v", b, err)
	}
}

func TestUnknownRoomIs404(t *testing.T) {
	_, c := newSynapseFake(t)
	for _, call := range []func() error{
		func() error { _, err := c.Room(context.Background(), "tok", "!nope:example.com"); return err },
		func() error { _, err := c.Members(context.Background(), "tok", "!nope:example.com"); return err },
	} {
		var se *Error
		if err := call(); !errors.As(err, &se) || se.Status != http.StatusNotFound || se.Errcode != "M_NOT_FOUND" {
			t.Errorf("%v", err)
		}
	}
}

// Synapse's purge defaults to true: Close must say false, every time.
func TestCloseAndDeleteAlwaysSendBlockAndPurge(t *testing.T) {
	f, c := newSynapseFake(t)
	ctx := context.Background()
	if id, err := c.Close(ctx, "tok", grp); err != nil || id != "D1" {
		t.Fatalf("close %q %v", id, err)
	}
	if id, err := c.Delete(ctx, "tok", grp); err != nil || id != "D2" {
		t.Fatalf("delete %q %v", id, err)
	}
	if want := []string{`{"block":true,"purge":false}`, `{"block":true,"purge":true}`}; !reflect.DeepEqual(f.bodies, want) {
		t.Fatalf("bodies %v", f.bodies)
	}
}

func TestDeleteJobs(t *testing.T) {
	_, c := newSynapseFake(t)
	jobs, err := c.DeleteJobs(context.Background(), "tok", grp)
	if err != nil || !reflect.DeepEqual(jobs, []DeleteJob{{ID: "D1", Status: "complete"}, {ID: "D2", Status: "active"}}) {
		t.Fatalf("%+v %v", jobs, err)
	}
	none, err := c.DeleteJobs(context.Background(), "tok", "!gone:example.com")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("no task must be an empty list: %#v %v", none, err)
	}
}

func TestRoomMediaListsLocalOnlyAndDeleteIsIdempotent(t *testing.T) {
	f, c := newSynapseFake(t)
	ctx := context.Background()
	media, err := c.RoomMedia(ctx, "tok", grp)
	if err != nil || !reflect.DeepEqual(media, []Media{{"example.com", "AVATAR1"}, {"example.com", "Pic_2-b"}}) {
		t.Fatalf("%+v %v", media, err)
	}
	for _, m := range []Media{{"example.com", "AVATAR1"}, {"example.com", "GONE"}} {
		if err := c.DeleteMedia(ctx, "tok", m); err != nil {
			t.Errorf("%s: %v", m.ID, err)
		}
	}
	if !reflect.DeepEqual(f.deleted, []string{"AVATAR1"}) {
		t.Errorf("deleted %v", f.deleted)
	}
	if _, err := c.RoomMedia(ctx, "tok", "!odd:example.com"); err == nil {
		t.Error("a non-mxc media URI was accepted")
	}
}
