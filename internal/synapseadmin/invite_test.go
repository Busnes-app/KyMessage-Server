package synapseadmin

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

const bot = "@kymessages-notes:example.com"

// inviteRoom serves room !r's member, create and power-levels state and fetch_event for events,
// the bot's current join replacing head.
func inviteRoom(t *testing.T, head string, events map[string]string, create, levels string) *Client {
	return fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_synapse/admin/v1/rooms/!r:example.com/state":
			switch r.URL.Query().Get("type") {
			case "m.room.member":
				fmt.Fprintf(w, `{"state":[
					{"type":"m.room.member","room_id":"!r:example.com","sender":"@alice:example.com","state_key":"@alice:example.com","content":{"membership":"join"}},
					{"type":"m.room.member","room_id":"!r:example.com","sender":%q,"state_key":%q,"content":{"membership":"join"},"unsigned":{"replaces_state":%q}}]}`, bot, bot, head)
			case "m.room.create":
				fmt.Fprintf(w, `{"state":[%s]}`, create)
			case "m.room.power_levels":
				fmt.Fprintf(w, `{"state":[%s]}`, levels)
			default:
				http.Error(w, "type", http.StatusBadRequest)
			}
		default:
			id := r.URL.Path[len("/_synapse/admin/v1/fetch_event/"):]
			e, ok := events[id]
			if !ok {
				http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"event":%s}`, e)
		}
	})
}

func member(sender, membership, replaces string) string {
	return fmt.Sprintf(`{"type":"m.room.member","room_id":"!r:example.com","sender":%q,"state_key":%q,"content":{"membership":%q},"unsigned":{"replaces_state":%q}}`,
		sender, bot, membership, replaces)
}

const (
	createdByAlice = `{"type":"m.room.create","sender":"@alice:example.com","state_key":"","content":{"room_version":"12","additional_creators":["@carol:example.com"]}}`
	levels         = `{"type":"m.room.power_levels","sender":"@alice:example.com","state_key":"","content":{"users":{"@dana:example.com":100,"@bob:example.com":50},"users_default":0}}`
)

func TestAdminInvite(t *testing.T) {
	for _, tc := range []struct {
		name, head, inviter string
		events              map[string]string
		ok                  bool
	}{
		{"creator", "$i", "@alice:example.com", map[string]string{"$i": member("@alice:example.com", "invite", "")}, true},
		{"additional creator", "$i", "@carol:example.com", map[string]string{"$i": member("@carol:example.com", "invite", "")}, true},
		{"power level 100", "$i", "@dana:example.com", map[string]string{"$i": member("@dana:example.com", "invite", "")}, true},
		{"moderator", "$i", "@bob:example.com", map[string]string{"$i": member("@bob:example.com", "invite", "")}, false},
		{"default level", "$i", "@erin:example.com", map[string]string{"$i": member("@erin:example.com", "invite", "")}, false},
		// A profile change replaces the join with a join; the invite is further back.
		{"through a profile change", "$j", "@alice:example.com",
			map[string]string{"$j": member(bot, "join", "$i"), "$i": member("@alice:example.com", "invite", "")}, true},
		{"joined after leaving", "$l", "", map[string]string{"$l": member(bot, "leave", "$i"), "$i": member("@alice:example.com", "invite", "")}, false},
		{"no earlier membership", "", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := inviteRoom(t, tc.head, tc.events, createdByAlice, levels)
			inviter, ok, err := c.AdminInvite(context.Background(), "tok", "!r:example.com", bot)
			if err != nil || inviter != tc.inviter || ok != tc.ok {
				t.Fatalf("got %q %v %v, want %q %v", inviter, ok, err, tc.inviter, tc.ok)
			}
		})
	}
}

// An event for another user or room never counts as the bot's invite.
func TestAdminInviteRefusesForeignEvents(t *testing.T) {
	for name, e := range map[string]string{
		"other user": `{"type":"m.room.member","room_id":"!r:example.com","sender":"@alice:example.com","state_key":"@bob:example.com","content":{"membership":"invite"}}`,
		"other room": `{"type":"m.room.member","room_id":"!x:example.com","sender":"@alice:example.com","state_key":"` + bot + `","content":{"membership":"invite"}}`,
		"a message":  `{"type":"m.room.message","room_id":"!r:example.com","sender":"@alice:example.com","content":{"body":"hi"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := inviteRoom(t, "$i", map[string]string{"$i": e}, createdByAlice, levels)
			if inviter, ok, err := c.AdminInvite(context.Background(), "tok", "!r:example.com", bot); err != nil || ok || inviter != "" {
				t.Fatalf("got %q %v %v", inviter, ok, err)
			}
		})
	}
}

// A lookup failure is an error, never a refusal or an approval.
func TestAdminInviteErrors(t *testing.T) {
	c := inviteRoom(t, "$gone", map[string]string{}, createdByAlice, levels)
	if _, ok, err := c.AdminInvite(context.Background(), "tok", "!r:example.com", bot); err == nil || ok {
		t.Fatalf("got %v %v", ok, err)
	}
}
