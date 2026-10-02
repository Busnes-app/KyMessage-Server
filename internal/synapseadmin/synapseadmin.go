// Package synapseadmin calls Synapse's admin API on its internal listener. Every call carries
// a console session token minted for one action (matrixsync.Client.AsConsole); no error here
// carries the token.
package synapseadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
