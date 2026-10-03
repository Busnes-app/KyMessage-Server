// Package matrixrtc implements the Element Call authorization wire format and the small
// LiveKit room-management surface KyMessages needs. Clients encrypt media; this has no keys.
package matrixrtc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Hash is MSC4195's SHA-256 of a compact JSON string array, standard unpadded base64.
func Hash(parts ...string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(parts)
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// Client talks only to its operator-configured LiveKit origin, without proxies or redirects.
type Client struct {
	base, key, secret string
	http              *http.Client
}

func New(base, key, secret string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &Client{base, key, secret, &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) token(identity, metadata string, grant map[string]any) string {
	now := time.Now().Unix()
	claims, _ := json.Marshal(map[string]any{"iss": c.key, "sub": identity, "iat": now, "nbf": now - 5, "exp": now + 15, "video": grant, "metadata": metadata})
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(c.secret))
	_, _ = mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Join grants one room, with no administration, recording, or mutable metadata.
func (c *Client) Join(room, identity, mxid string) string {
	return c.token(identity, mxid, map[string]any{"roomJoin": true, "room": room, "canPublish": true, "canSubscribe": true, "canPublishData": true, "canUpdateOwnMetadata": false})
}

func (c *Client) call(ctx context.Context, method string, grant map[string]any, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/twirp/livekit.RoomService/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token("", "", grant))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("LiveKit %s unavailable", method)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LiveKit %s returned %d", method, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Create stores the Matrix room ID as immutable server-owned room metadata.
func (c *Client) Create(ctx context.Context, name, roomID string) error {
	return c.call(ctx, "CreateRoom", map[string]any{"roomCreate": true}, map[string]any{"name": name, "metadata": roomID, "empty_timeout": 300, "departure_timeout": 20}, nil)
}

type Room struct {
	Name     string `json:"name"`
	Metadata string `json:"metadata"`
}
type Participant struct {
	Identity string `json:"identity"`
	Metadata string `json:"metadata"`
}

func (c *Client) Rooms(ctx context.Context) ([]Room, error) {
	var out struct {
		Rooms []Room `json:"rooms"`
	}
	err := c.call(ctx, "ListRooms", map[string]any{"roomList": true}, map[string]any{}, &out)
	return out.Rooms, err
}
func (c *Client) Participants(ctx context.Context, room string) ([]Participant, error) {
	var out struct {
		Participants []Participant `json:"participants"`
	}
	err := c.call(ctx, "ListParticipants", map[string]any{"roomAdmin": true, "room": room}, map[string]string{"room": room}, &out)
	return out.Participants, err
}
func (c *Client) Remove(ctx context.Context, room, identity string) error {
	return c.call(ctx, "RemoveParticipant", map[string]any{"roomAdmin": true, "room": room}, map[string]string{"room": room, "identity": identity}, nil)
}
