package synapseadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Meeting rooms (KyCalendar): the console account creates them and manages membership, name
// and the organizer's power level through the client API. It never reads or sends messages.

// Power levels of a meeting room. The console creates the room: in room version 12 a creator
// outranks every level and is never listed in users.
const (
	OrganizerLevel = 50
	callMemberType = "org.matrix.msc3401.call.member"
)

// meetingEvents is Synapse's default for room version 12 plus what stock Element adds while
// Element Call is on: any member may join the call.
var meetingEvents = map[string]int{
	"m.room.name": 50, "m.room.power_levels": 100, "m.room.history_visibility": 100,
	"m.room.canonical_alias": 50, "m.room.avatar": 50, "m.room.tombstone": 150,
	"m.room.server_acl": 100, "m.room.encryption": 100, callMemberType: 0,
}

func clientRoomPath(id string) string { return clientPrefix + "rooms/" + url.PathEscape(id) }

// CreateMeetingRoom creates an encrypted, invite-only room named name in which organizer has
// OrganizerLevel and everyone else 0; inviting and removing need OrganizerLevel. Nobody is
// invited yet. History is visible from joining.
func (c *Client) CreateMeetingRoom(ctx context.Context, token, name, organizer string) (string, error) {
	state := func(typ string, content any) map[string]any {
		return map[string]any{"type": typ, "state_key": "", "content": content}
	}
	req := map[string]any{
		"preset": "private_chat",
		"name":   name,
		"initial_state": []any{
			state("m.room.join_rules", map[string]string{"join_rule": "invite"}),
			state("m.room.history_visibility", map[string]string{"history_visibility": "joined"}),
			state("m.room.guest_access", map[string]string{"guest_access": "forbidden"}),
			state("m.room.encryption", map[string]string{"algorithm": "m.megolm.v1.aes-sha2"}),
		},
		"power_level_content_override": map[string]any{
			"users": map[string]int{organizer: OrganizerLevel}, "users_default": 0, "events_default": 0,
			"state_default": 50, "invite": OrganizerLevel, "kick": OrganizerLevel, "ban": 50, "redact": 50,
			"events": meetingEvents,
		},
	}
	var r struct {
		RoomID string `json:"room_id"`
	}
	if err := c.do(ctx, token, http.MethodPost, clientPrefix+"createRoom", req, &r); err != nil {
		return "", err
	}
	return r.RoomID, nil
}

// Memberships maps each user with a membership event in room to its state (join, invite,
// leave, ban, knock). It reads state only.
func (c *Client) Memberships(ctx context.Context, token, room string) (map[string]string, error) {
	events, err := c.state(ctx, token, room, "m.room.member")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(events))
	for _, e := range events {
		if e.StateKey != nil {
			out[*e.StateKey] = membership(e)
		}
	}
	return out, nil
}

// Invite invites user to room.
func (c *Client) Invite(ctx context.Context, token, room, user string) error {
	return c.do(ctx, token, http.MethodPost, clientRoomPath(room)+"/invite", map[string]string{"user_id": user}, nil)
}

// Kick removes user from room, invited or joined; they may be invited again.
func (c *Client) Kick(ctx context.Context, token, room, user string) error {
	return c.do(ctx, token, http.MethodPost, clientRoomPath(room)+"/kick", map[string]string{"user_id": user}, nil)
}

// SetRoomName sets room's name.
func (c *Client) SetRoomName(ctx context.Context, token, room, name string) error {
	return c.do(ctx, token, http.MethodPut, clientRoomPath(room)+"/state/m.room.name/", map[string]string{"name": name}, nil)
}

// SetOrganizer makes organizer the only user at OrganizerLevel, keeping the rest of the
// room's power levels as they are.
func (c *Client) SetOrganizer(ctx context.Context, token, room, organizer string) error {
	levels, err := c.state(ctx, token, room, "m.room.power_levels")
	if err != nil {
		return err
	}
	if len(levels) != 1 {
		return errors.New("room has no power levels")
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal(levels[0].Content, &content); err != nil {
		return err
	}
	users := map[string]json.Number{}
	if raw, ok := content["users"]; ok {
		if err := json.Unmarshal(raw, &users); err != nil {
			return err
		}
	}
	level := json.Number(strconv.Itoa(OrganizerLevel))
	for u, l := range users {
		if l == level {
			delete(users, u)
		}
	}
	users[organizer] = level
	raw, err := json.Marshal(users)
	if err != nil {
		return err
	}
	content["users"] = raw
	return c.do(ctx, token, http.MethodPut, clientRoomPath(room)+"/state/m.room.power_levels/", content, nil)
}

// ExemptFromRatelimit lifts Synapse's per-user rate limits (messages, invites) for user, so a
// 100-person meeting is not throttled. Repeating it is harmless.
func (c *Client) ExemptFromRatelimit(ctx context.Context, token, user string) error {
	return c.do(ctx, token, http.MethodPost, prefix+"v1/users/"+url.PathEscape(user)+"/override_ratelimit",
		map[string]int{"messages_per_second": 0, "burst_count": 0}, nil)
}
