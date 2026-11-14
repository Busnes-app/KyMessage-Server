package synapseadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
)

// meetingSynapse records each request as "METHOD path" and its JSON body.
func meetingSynapse(t *testing.T, reply map[string]string) (*Client, *[]string, map[string]any) {
	t.Helper()
	var calls []string
	bodies := map[string]any{}
	c := fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		calls = append(calls, key)
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			var v any
			if err := json.Unmarshal(b, &v); err != nil {
				t.Errorf("%s: %v", key, err)
			}
			bodies[key] = v
		}
		fmt.Fprint(w, reply[key])
	})
	return c, &calls, bodies
}

func TestCreateMeetingRoom(t *testing.T) {
	c, calls, bodies := meetingSynapse(t, map[string]string{"POST /_matrix/client/v3/createRoom": `{"room_id":"!m:example.com"}`})
	id, err := c.CreateMeetingRoom(context.Background(), "tok", "Weekly sync", "@alice:example.com")
	if err != nil || id != "!m:example.com" || !reflect.DeepEqual(*calls, []string{"POST /_matrix/client/v3/createRoom"}) {
		t.Fatalf("%q %v %v", id, err, *calls)
	}
	var want any
	_ = json.Unmarshal([]byte(`{
		"preset": "private_chat", "name": "Weekly sync",
		"initial_state": [
			{"type": "m.room.join_rules", "state_key": "", "content": {"join_rule": "invite"}},
			{"type": "m.room.history_visibility", "state_key": "", "content": {"history_visibility": "joined"}},
			{"type": "m.room.guest_access", "state_key": "", "content": {"guest_access": "forbidden"}},
			{"type": "m.room.encryption", "state_key": "", "content": {"algorithm": "m.megolm.v1.aes-sha2"}}
		],
		"power_level_content_override": {
			"users": {"@alice:example.com": 50}, "users_default": 0, "events_default": 0, "state_default": 50,
			"invite": 50, "kick": 50, "ban": 50, "redact": 50,
			"events": {"m.room.name": 50, "m.room.power_levels": 100, "m.room.history_visibility": 100,
				"m.room.canonical_alias": 50, "m.room.avatar": 50, "m.room.tombstone": 150,
				"m.room.server_acl": 100, "m.room.encryption": 100, "org.matrix.msc3401.call.member": 0}
		}
	}`), &want)
	if got := bodies["POST /_matrix/client/v3/createRoom"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("body:\n%v\nwant\n%v", got, want)
	}
}

func TestMeetingMembership(t *testing.T) {
	room := "!m:example.com"
	c, calls, bodies := meetingSynapse(t, map[string]string{
		"GET /_synapse/admin/v1/rooms/%21m:example.com/state?type=m.room.member": `{"state":[
			{"type":"m.room.member","state_key":"@a:example.com","content":{"membership":"join"}},
			{"type":"m.room.member","state_key":"@b:example.com","content":{"membership":"invite"}},
			{"type":"m.room.member","state_key":"@c:example.com","content":{"membership":"leave"}}]}`,
	})
	ctx := context.Background()
	got, err := c.Memberships(ctx, "tok", room)
	if want := map[string]string{"@a:example.com": "join", "@b:example.com": "invite", "@c:example.com": "leave"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("memberships %v %v", got, err)
	}
	for _, err := range []error{
		c.Invite(ctx, "tok", room, "@c:example.com"),
		c.Kick(ctx, "tok", room, "@b:example.com"),
		c.SetRoomName(ctx, "tok", room, "New"),
		c.ExemptFromRatelimit(ctx, "tok", "@kymessages-console:example.com"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"GET /_synapse/admin/v1/rooms/%21m:example.com/state?type=m.room.member",
		"POST /_matrix/client/v3/rooms/%21m:example.com/invite",
		"POST /_matrix/client/v3/rooms/%21m:example.com/kick",
		"PUT /_matrix/client/v3/rooms/%21m:example.com/state/m.room.name/",
		"POST /_synapse/admin/v1/users/@kymessages-console:example.com/override_ratelimit",
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls %v", *calls)
	}
	if b := bodies[want[1]]; !reflect.DeepEqual(b, map[string]any{"user_id": "@c:example.com"}) {
		t.Fatalf("invite %v", b)
	}
	if b := bodies[want[3]]; !reflect.DeepEqual(b, map[string]any{"name": "New"}) {
		t.Fatalf("name %v", b)
	}
	if b := bodies[want[4]]; !reflect.DeepEqual(b, map[string]any{"messages_per_second": 0.0, "burst_count": 0.0}) {
		t.Fatalf("ratelimit %v", b)
	}
}

// Only the new organizer holds level 50 afterwards; every other field stays as it was.
func TestSetOrganizer(t *testing.T) {
	get := "GET /_synapse/admin/v1/rooms/%21m:example.com/state?type=m.room.power_levels"
	put := "PUT /_matrix/client/v3/rooms/%21m:example.com/state/m.room.power_levels/"
	c, _, bodies := meetingSynapse(t, map[string]string{get: `{"state":[{"type":"m.room.power_levels","state_key":"",
		"content":{"users":{"@old:example.com":50,"@mod:example.com":75},"invite":50,"events":{"m.room.name":50}}}]}`})
	if err := c.SetOrganizer(context.Background(), "tok", "!m:example.com", "@new:example.com"); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"users": map[string]any{"@new:example.com": 50.0, "@mod:example.com": 75.0}, "invite": 50.0, "events": map[string]any{"m.room.name": 50.0}}
	if got := bodies[put]; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

// Of the client API only createRoom and rooms/ are reachable.
func TestPathGuardAdmitsOnlyMeetingRoutes(t *testing.T) {
	for p, want := range map[string]bool{
		"/_synapse/admin/v1/rooms": true, "/_matrix/client/v3/createRoom": true, "/_matrix/client/v3/rooms/!r:x/invite": true,
		"/_matrix/client/v3/account/whoami": false, "/_matrix/client/v3/sync": false, "/_matrix/media/v3/config": false,
		"/_matrix/client/v3/createRoomX": false,
	} {
		if allowedPath(p) != want {
			t.Errorf("%s: want %v", p, want)
		}
	}
}
