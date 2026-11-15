package synapseadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// Meeting guests (docs/KYCALENDAR.md "Guests"). The console marks a guest account in its
// meeting room; Synapse's confinement module (matrixinit ky_guests.py) lets a guest- account act
// only where the console's marker names it. The guest's own token sets its name and knocks.

// GuestMarker is the state event type the confinement module trusts, keyed by localpart.
const GuestMarker = "app.kymessages.guest"

// AllowKnocks makes room a waiting room: join rule knock, and invite at 0 so any member may
// admit a knock. Nothing is sent for what already holds.
func (c *Client) AllowKnocks(ctx context.Context, token, room string) error {
	rules, err := c.state(ctx, token, room, "m.room.join_rules")
	if err != nil {
		return err
	}
	var jr struct {
		JoinRule string `json:"join_rule"`
	}
	if len(rules) == 1 {
		_ = json.Unmarshal(rules[0].Content, &jr)
	}
	if jr.JoinRule != "knock" {
		if err := c.do(ctx, token, http.MethodPut, clientRoomPath(room)+"/state/m.room.join_rules/", map[string]string{"join_rule": "knock"}, nil); err != nil {
			return err
		}
	}
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
	if string(content["invite"]) == "0" {
		return nil
	}
	content["invite"] = json.RawMessage("0")
	return c.do(ctx, token, http.MethodPut, clientRoomPath(room)+"/state/m.room.power_levels/", content, nil)
}

// MarkGuest names localpart in room as one of its guests.
func (c *Client) MarkGuest(ctx context.Context, token, room, localpart string) error {
	return c.do(ctx, token, http.MethodPut, clientRoomPath(room)+"/state/"+GuestMarker+"/"+url.PathEscape(localpart), map[string]string{}, nil)
}

// SetDisplayName sets user's profile name with user's own token.
func (c *Client) SetDisplayName(ctx context.Context, token, user, name string) error {
	return c.do(ctx, token, http.MethodPut, clientPrefix+"profile/"+url.PathEscape(user)+"/displayname", map[string]string{"displayname": name}, nil)
}

// Knock asks to join room with the knocking user's own token.
func (c *Client) Knock(ctx context.Context, token, room string) error {
	return c.do(ctx, token, http.MethodPost, clientPrefix+"knock/"+url.PathEscape(room), map[string]string{}, nil)
}

// Member is one membership in a room and who set it (the admitting member of an invite).
type Member struct{ Membership, Sender string }

// MemberStates maps each user with a membership event in room to it. It reads state only.
func (c *Client) MemberStates(ctx context.Context, token, room string) (map[string]Member, error) {
	events, err := c.state(ctx, token, room, "m.room.member")
	if err != nil {
		return nil, err
	}
	out := make(map[string]Member, len(events))
	for _, e := range events {
		if e.StateKey != nil {
			out[*e.StateKey] = Member{membership(e), e.Sender}
		}
	}
	return out, nil
}
