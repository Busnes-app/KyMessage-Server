// Package synapseadmin calls Synapse's admin API on its internal listener. Every call carries
// a console session token minted for one action (matrixsync.Client.AsConsole); no error here
// carries the token.
package synapseadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const prefix = "/_synapse/admin/"

// Error is a non-2xx answer with Synapse's errcode and message (clipped).
type Error struct {
	Method, Path     string
	Status           int
	Errcode, Message string
}

func (e *Error) Error() string {
	s := fmt.Sprintf("Synapse %s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Errcode != "" {
		s += " " + e.Errcode
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Client is safe for concurrent use.
type Client struct {
	base string
	hc   *http.Client
}

// New returns a client for Synapse's internal origin. It ignores proxy variables and follows
// no redirects, so the token goes to base or nowhere.
func New(base string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &Client{base: strings.TrimSuffix(base, "/"), hc: &http.Client{
		Transport:     tr,
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// do sends one admin request; path must start with prefix.
func (c *Client) do(ctx context.Context, token, method, path string, body, out any) error {
	if !strings.HasPrefix(path, prefix) {
		return fmt.Errorf("refusing admin path %q", path)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("Synapse %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		e := &Error{Method: method, Path: path, Status: resp.StatusCode}
		var b struct {
			Errcode string `json:"errcode"`
			Error   string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&b) == nil {
			e.Errcode, e.Message = clip(b.Errcode, 64), clip(b.Error, 200)
		}
		return e
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// Room is one room as Synapse's admin API reports it. Synapse sends null for an unset name,
// alias, encryption or join rule; those read as "".
type Room struct {
	ID           string `json:"room_id"`
	Name         string `json:"name"`
	Alias        string `json:"canonical_alias"`
	Creator      string `json:"creator"`
	Members      int    `json:"joined_members"`
	LocalMembers int    `json:"joined_local_members"`
	Encryption   string `json:"encryption"` // the algorithm; "" when not encrypted
	Public       bool   `json:"public"`
	JoinRule     string `json:"join_rules"`
	StateEvents  int    `json:"state_events"` // the only size-like count Synapse reports
}

type RoomQuery struct {
	Offset, Limit int
	Search        string // name, alias substring or exact room ID; "" lists all
}

type RoomPage struct {
	Rooms []Room
	Total int
}

// Rooms lists rooms ordered by name.
func (c *Client) Rooms(ctx context.Context, token string, q RoomQuery) (RoomPage, error) {
	v := url.Values{"from": {strconv.Itoa(q.Offset)}, "limit": {strconv.Itoa(q.Limit)}, "order_by": {"name"}}
	if q.Search != "" {
		v.Set("search_term", q.Search)
	}
	var page struct {
		Rooms []Room `json:"rooms"`
		Total int    `json:"total_rooms"`
	}
	if err := c.do(ctx, token, http.MethodGet, prefix+"v1/rooms?"+v.Encode(), nil, &page); err != nil {
		return RoomPage{}, err
	}
	return RoomPage{Rooms: page.Rooms, Total: page.Total}, nil
}

func roomPath(version, id string) string { return prefix + version + "/rooms/" + url.PathEscape(id) }

// Room reads one room; *Error with Status 404 when Synapse does not know it.
func (c *Client) Room(ctx context.Context, token, id string) (Room, error) {
	var r Room
	err := c.do(ctx, token, http.MethodGet, roomPath("v1", id), nil, &r)
	return r, err
}

// Members are the room's joined members (user IDs).
func (c *Client) Members(ctx context.Context, token, id string) ([]string, error) {
	var m struct {
		Members []string `json:"members"`
	}
	err := c.do(ctx, token, http.MethodGet, roomPath("v1", id)+"/members", nil, &m)
	return m.Members, err
}

// Blocked reports whether joining the room is refused.
func (c *Client) Blocked(ctx context.Context, token, id string) (bool, error) {
	var b struct {
		Block bool `json:"block"`
	}
	err := c.do(ctx, token, http.MethodGet, roomPath("v1", id)+"/block", nil, &b)
	return b.Block, err
}

// Close starts Synapse's shutdown without a purge: the room is blocked, every local member
// leaves, history stays. It returns the background job's delete_id.
func (c *Client) Close(ctx context.Context, token, id string) (string, error) {
	return c.shutdown(ctx, token, id, false)
}

// Delete starts a shutdown that also purges the room's events and state; the room stays
// blocked. Media is not purged.
func (c *Client) Delete(ctx context.Context, token, id string) (string, error) {
	return c.shutdown(ctx, token, id, true)
}

func (c *Client) shutdown(ctx context.Context, token, id string, purge bool) (string, error) {
	var r struct {
		DeleteID string `json:"delete_id"`
	}
	// Synapse's purge defaults to true: always send it.
	if err := c.do(ctx, token, http.MethodDelete, roomPath("v2", id), map[string]bool{"block": true, "purge": purge}, &r); err != nil {
		return "", err
	}
	if r.DeleteID == "" {
		return "", errors.New("Synapse started no delete job")
	}
	return r.DeleteID, nil
}

// DeleteJob is one close or delete job. Synapse gives no reason when one fails.
type DeleteJob struct{ ID, Status string }

// DeleteJobs lists the room's jobs that have started (active, complete, failed); Synapse does
// not list one still waiting to start. None is an empty list.
func (c *Client) DeleteJobs(ctx context.Context, token, id string) ([]DeleteJob, error) {
	var r struct {
		Results []struct {
			DeleteID string `json:"delete_id"`
			Status   string `json:"status"`
		} `json:"results"`
	}
	err := c.do(ctx, token, http.MethodGet, roomPath("v2", id)+"/delete_status", nil, &r)
	var se *Error
	if errors.As(err, &se) && se.Status == http.StatusNotFound && se.Errcode == "M_NOT_FOUND" {
		return []DeleteJob{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]DeleteJob, 0, len(r.Results))
	for _, j := range r.Results {
		out = append(out, DeleteJob{ID: j.DeleteID, Status: j.Status})
	}
	return out, nil
}

// stateEvent is the part of a state event the inviter check reads.
type stateEvent struct {
	Type     string          `json:"type"`
	RoomID   string          `json:"room_id"`
	Sender   string          `json:"sender"`
	StateKey *string         `json:"state_key"`
	Content  json.RawMessage `json:"content"`
	Unsigned struct {
		ReplacesState string `json:"replaces_state"`
	} `json:"unsigned"`
}

func (c *Client) state(ctx context.Context, token, room, typ string) ([]stateEvent, error) {
	var r struct {
		State []stateEvent `json:"state"`
	}
	err := c.do(ctx, token, http.MethodGet, roomPath("v1", room)+"/state?"+url.Values{"type": {typ}}.Encode(), nil, &r)
	return r.State, err
}

// AdminInvite reports who invited user into room, and whether that inviter is the room's
// creator (m.room.create sender or additional_creators) or has power level 100. It walks
// user's current join back through its own earlier joins (a profile change replaces the join)
// to the invite it replaced; a join reached any other way has no inviter and is refused.
// It reads only state events and user's own membership events, never messages.
func (c *Client) AdminInvite(ctx context.Context, token, room, user string) (inviter string, ok bool, err error) {
	members, err := c.state(ctx, token, room, "m.room.member")
	if err != nil {
		return "", false, err
	}
	var prev string
	for _, e := range members {
		if e.StateKey != nil && *e.StateKey == user && membership(e) == "join" {
			prev = e.Unsigned.ReplacesState
		}
	}
	for hops := 0; prev != "" && hops < 20; hops++ {
		var r struct {
			Event stateEvent `json:"event"`
		}
		if err := c.do(ctx, token, http.MethodGet, prefix+"v1/fetch_event/"+url.PathEscape(prev), nil, &r); err != nil {
			return "", false, err
		}
		e := r.Event
		if e.Type != "m.room.member" || e.RoomID != room || e.StateKey == nil || *e.StateKey != user {
			return "", false, nil
		}
		m := membership(e)
		if m == "invite" {
			inviter = e.Sender
		}
		if m != "join" {
			break
		}
		prev = e.Unsigned.ReplacesState
	}
	if inviter == "" {
		return "", false, nil
	}
	create, err := c.state(ctx, token, room, "m.room.create")
	if err != nil || len(create) != 1 {
		return inviter, false, err
	}
	levels, err := c.state(ctx, token, room, "m.room.power_levels")
	if err != nil {
		return inviter, false, err
	}
	return inviter, roomAdmin(create[0], levels, inviter), nil
}

func membership(e stateEvent) string {
	var m struct {
		Membership string `json:"membership"`
	}
	_ = json.Unmarshal(e.Content, &m)
	return m.Membership
}

// roomAdmin: a creator, or power level 100 in the room's power levels (users_default otherwise).
func roomAdmin(create stateEvent, levels []stateEvent, user string) bool {
	var c struct {
		Additional []string `json:"additional_creators"`
	}
	_ = json.Unmarshal(create.Content, &c)
	if user == create.Sender || slices.Contains(c.Additional, user) {
		return true
	}
	if len(levels) != 1 {
		return false
	}
	var pl struct {
		Users   map[string]json.Number `json:"users"`
		Default json.Number            `json:"users_default"`
	}
	if json.Unmarshal(levels[0].Content, &pl) != nil {
		return false
	}
	level, ok := pl.Users[user]
	if !ok {
		level = pl.Default
	}
	n, err := level.Int64()
	return err == nil && n >= 100
}
