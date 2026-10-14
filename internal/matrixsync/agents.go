package matrixsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"
)

// ErrNameTaken: MAS already holds the username.
var ErrNameTaken = errors.New("matrix name is taken")

// agentScope is an agent token's whole power: the client API on one fixed device. Never
// urn:synapse:admin:* or urn:mas:admin.
func agentScope(deviceID string) string {
	return "urn:matrix:client:api:* urn:matrix:client:device:" + deviceID
}

// Activity is an agent session's creation and last use (nil when never used).
type Activity struct {
	CreatedAt  time.Time
	LastActive *time.Time
}

// Since is when the session was last used, or minted if never.
func (a Activity) Since() time.Time {
	if a.LastActive != nil {
		return *a.LastActive
	}
	return a.CreatedAt
}

// CreateAgentUser creates a MAS user with no password and no upstream link. An existing
// username is ErrNameTaken: an agent never adopts an account.
func (c *Client) CreateAgentUser(ctx context.Context, username string) (string, error) {
	body, err := json.Marshal(map[string]string{"username": username})
	if err != nil {
		return "", err
	}
	var doc struct {
		Data resource `json:"data"`
	}
	err = c.call(ctx, http.MethodPost, adminPrefix+"users", body, &doc)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusConflict {
		return "", ErrNameTaken
	}
	if err != nil {
		return "", fmt.Errorf("agent account: %w", err)
	}
	if doc.Data.ID == "" {
		return "", errors.New("agent account: MAS returned no user ID")
	}
	return doc.Data.ID, nil
}

type personalSession struct {
	Data struct {
		ID         string `json:"id"`
		Attributes struct {
			AccessToken  string     `json:"access_token"`
			CreatedAt    time.Time  `json:"created_at"`
			LastActiveAt *time.Time `json:"last_active_at"`
			RevokedAt    *time.Time `json:"revoked_at"`
		} `json:"attributes"`
	} `json:"data"`
}

// MintAgentToken creates the agent's personal session with no expiry: KyMessages expires it
// after 90 days unused (ExpireIdleAgents). MAS creates the device on Synapse at once. Detached, as in AsConsole: a session MAS creates
// while ctx ends is still known, and one returned without a token is revoked.
func (c *Client) MintAgentToken(ctx context.Context, userID, deviceID string) (string, string, error) {
	body, err := json.Marshal(map[string]any{"actor_user_id": userID, "human_name": "KyMessages agent", "scope": agentScope(deviceID)})
	if err != nil {
		return "", "", err
	}
	var doc personalSession
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ConsoleCallTimeout)
	defer cancel()
	if err := c.call(mctx, http.MethodPost, adminPrefix+"personal-sessions", body, &doc); err != nil {
		return "", "", fmt.Errorf("agent token: %w", err)
	}
	return c.tokenOrRevoke(ctx, doc)
}

func (c *Client) tokenOrRevoke(ctx context.Context, doc personalSession) (string, string, error) {
	switch {
	case doc.Data.ID == "":
		return "", "", errors.New("agent token: MAS returned no session ID")
	case doc.Data.Attributes.AccessToken == "":
		if err := c.RevokeAgentSession(ctx, doc.Data.ID); err != nil {
			log.Printf("[MATRIX] agent session %s returned without a token was not revoked: %v", doc.Data.ID, err)
		}
		return "", "", errors.New("agent token: MAS returned no token")
	}
	return doc.Data.ID, doc.Data.Attributes.AccessToken, nil
}

// RotateAgentToken gives the agent a new token. With a live session it regenerates it: same
// session and device (so the agent keeps its encryption keys), no expiry; Synapse may accept
// the old token for up to 2 minutes, its token cache. Without one (revoked or expired, which
// deleted the device) it mints a new session; the caller passes a new device ID then.
func (c *Client) RotateAgentToken(ctx context.Context, userID, deviceID, sessionID string) (string, string, error) {
	if sessionID == "" {
		return c.MintAgentToken(ctx, userID, deviceID)
	}
	var doc personalSession
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ConsoleCallTimeout)
	defer cancel()
	if err := c.call(rctx, http.MethodPost, adminPrefix+"personal-sessions/"+url.PathEscape(sessionID)+"/regenerate", []byte(`{}`), &doc); err != nil {
		return "", "", fmt.Errorf("agent token: %w", err)
	}
	if doc.Data.ID != sessionID {
		return "", "", errors.New("agent token: MAS regenerated another session")
	}
	return c.tokenOrRevoke(ctx, doc)
}

// RevokeAgentSession ends a personal session at once; one already revoked counts as done.
// Synapse then deletes the device and its encryption keys.
func (c *Client) RevokeAgentSession(ctx context.Context, sessionID string) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ConsoleCallTimeout)
	defer cancel()
	err := c.call(rctx, http.MethodPost, adminPrefix+"personal-sessions/"+url.PathEscape(sessionID)+"/revoke", nil, nil)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusConflict {
		return nil
	}
	return err
}

// AgentActivity reads a personal session's creation and last use.
func (c *Client) AgentActivity(ctx context.Context, sessionID string) (Activity, error) {
	var doc personalSession
	if err := c.call(ctx, http.MethodGet, adminPrefix+"personal-sessions/"+url.PathEscape(sessionID), nil, &doc); err != nil {
		return Activity{}, err
	}
	a := doc.Data.Attributes
	return Activity{CreatedAt: a.CreatedAt, LastActive: a.LastActiveAt}, nil
}
