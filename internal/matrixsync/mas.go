package matrixsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const adminPrefix = "/api/admin/v1/"

// StatusError is a non-2xx answer from the admin API. MAS's body is not kept: callers that
// need more ask MAS again.
type StatusError struct {
	Method, Path string
	Status       int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("MAS %s %s: HTTP %d", e.Method, e.Path, e.Status)
}

// Client calls MAS's admin API with a client-credentials token, cached until shortly before
// it expires and refetched once on a 401.
type Client struct {
	consoleMu        chan struct{} // Serialize first-use account creation; validate again on every use.
	base, id, secret string
	hc               *http.Client

	tokenMu chan struct{} // one-slot lock, so a waiter can give up with its context
	token   string
	expires time.Time
}

func NewClient(baseURL, clientID, secret string) *Client {
	// Never through a proxy from the environment: it would see the admin credentials.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &Client{base: strings.TrimSuffix(baseURL, "/"), id: clientID, secret: secret, tokenMu: make(chan struct{}, 1), consoleMu: make(chan struct{}, 1), hc: &http.Client{
		Transport:     tr,
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	select {
	case c.tokenMu <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-c.tokenMu }()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"urn:mas:admin"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(c.id), url.QueryEscape(c.secret))
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("MAS token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MAS token: HTTP %d", resp.StatusCode) // body may echo credentials
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&t); err != nil || t.AccessToken == "" {
		return "", errors.New("MAS token: unusable response")
	}
	c.token, c.expires = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second-30*time.Second)
	return c.token, nil
}

// dropToken forgets the cached token; on a done ctx it skips, as the retry fails anyway.
func (c *Client) dropToken(ctx context.Context) {
	select {
	case c.tokenMu <- struct{}{}:
		c.token = ""
		<-c.tokenMu
	case <-ctx.Done():
	}
}

// call sends one admin request; path must start with adminPrefix.
func (c *Client) call(ctx context.Context, method, path string, body []byte, out any) error {
	if !strings.HasPrefix(path, adminPrefix) {
		return fmt.Errorf("refusing admin path %q", path)
	}
	for attempt := 0; ; attempt++ {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return fmt.Errorf("MAS %s %s: %w", method, path, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			c.dropToken(ctx)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return &StatusError{Method: method, Path: path, Status: resp.StatusCode}
		}
		if out == nil {
			return nil
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
}

type resource struct {
	ID         string          `json:"id"`
	Attributes json.RawMessage `json:"attributes"`
}

// list follows links.next from path; every page must stay on the admin API.
func (c *Client) list(ctx context.Context, path string, each func(resource) error) error {
	for path != "" {
		var page struct {
			Data  []resource `json:"data"`
			Links struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		if err := c.call(ctx, http.MethodGet, path, nil, &page); err != nil {
			return err
		}
		for _, r := range page.Data {
			if err := each(r); err != nil {
				return err
			}
		}
		path = page.Links.Next
	}
	return nil
}

// Users lists every MAS user with the subject of its KyIdentity link. MAS must have exactly
// one upstream provider: links from any other source would be guesses.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var providers []string
	if err := c.list(ctx, adminPrefix+"upstream-oauth-providers?page[first]=100", func(r resource) error {
		providers = append(providers, r.ID)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(providers) != 1 {
		return nil, fmt.Errorf("MAS has %d upstream providers, want exactly KyIdentity", len(providers))
	}
	subjects := map[string]string{}
	ambiguous := map[string]bool{}
	if err := c.list(ctx, adminPrefix+"upstream-oauth-links?filter[provider]="+url.QueryEscape(providers[0])+"&page[first]=100", func(r resource) error {
		var a struct {
			Subject string  `json:"subject"`
			UserID  *string `json:"user_id"`
		}
		if err := json.Unmarshal(r.Attributes, &a); err != nil {
			return err
		}
		if a.UserID != nil {
			if prev, ok := subjects[*a.UserID]; ok && prev != a.Subject {
				ambiguous[*a.UserID] = true
			}
			subjects[*a.UserID] = a.Subject
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var users []User
	err := c.list(ctx, adminPrefix+"users?page[first]=100", func(r resource) error {
		var a userAttrs
		if err := json.Unmarshal(r.Attributes, &a); err != nil {
			return err
		}
		u := User{ID: r.ID, Username: a.Username, Subject: subjects[r.ID], Ambiguous: ambiguous[r.ID],
			Locked: a.LockedAt != nil, Deactivated: a.DeactivatedAt != nil}
		if u.Ambiguous {
			u.Subject = ""
		}
		users = append(users, u)
		return nil
	})
	return users, err
}

func (c *Client) post(ctx context.Context, id, op string, body []byte) error {
	return c.call(ctx, http.MethodPost, adminPrefix+"users/"+url.PathEscape(id)+"/"+op, body, nil)
}

func (c *Client) Lock(ctx context.Context, id string) error   { return c.post(ctx, id, "lock", nil) }
func (c *Client) Unlock(ctx context.Context, id string) error { return c.post(ctx, id, "unlock", nil) }

// Deactivate ends the user's sessions and removes them from rooms; their messages stay.
func (c *Client) Deactivate(ctx context.Context, id string) error {
	return c.post(ctx, id, "deactivate", []byte(`{"skip_erase":true}`))
}

type userAttrs struct {
	Username      string  `json:"username"`
	Admin         bool    `json:"admin"`
	LockedAt      *string `json:"locked_at"`
	DeactivatedAt *string `json:"deactivated_at"`
}

// one fetches a single resource from path.
func (c *Client) one(ctx context.Context, path string) (resource, error) {
	var doc struct {
		Data resource `json:"data"`
	}
	err := c.call(ctx, http.MethodGet, path, nil, &doc)
	return doc.Data, err
}

// User fetches one MAS user. Subject is left empty; Users resolves links.
func (c *Client) User(ctx context.Context, id string) (User, error) {
	r, err := c.one(ctx, adminPrefix+"users/"+url.PathEscape(id))
	if err != nil {
		return User{}, err
	}
	var a userAttrs
	if err := json.Unmarshal(r.Attributes, &a); err != nil {
		return User{}, err
	}
	return User{ID: r.ID, Username: a.Username, Locked: a.LockedAt != nil, Deactivated: a.DeactivatedAt != nil}, nil
}

// SessionKind names one of MAS's three session lists.
type SessionKind string

const (
	OAuth2Session  SessionKind = "oauth2"  // a Matrix client such as Element (native OIDC)
	CompatSession  SessionKind = "compat"  // a legacy Matrix login
	BrowserSession SessionKind = "browser" // the person's web session at MAS itself
)

var sessionPaths = map[SessionKind]string{
	OAuth2Session:  "oauth2-sessions",
	CompatSession:  "compat-sessions",
	BrowserSession: "user-sessions",
}

// ParseSessionKind accepts exactly the three kinds.
func ParseSessionKind(s string) (SessionKind, bool) {
	_, ok := sessionPaths[SessionKind(s)]
	return SessionKind(s), ok
}

// Session is one MAS session as the console shows it.
type Session struct {
	Kind       SessionKind
	ID, UserID string
	Device     string // Matrix device ID; empty for browser sessions
	Client     string // MAS's human name for the session, else the user agent
	IP         string
	CreatedAt  time.Time
	// LastActiveAt and FinishedAt are nil when MAS has none.
	LastActiveAt, FinishedAt *time.Time
}

type sessionAttrs struct {
	UserID       *string    `json:"user_id"`
	DeviceID     *string    `json:"device_id"`
	Scope        string     `json:"scope"`
	HumanName    *string    `json:"human_name"`
	UserAgent    *string    `json:"user_agent"`
	LastActiveIP *string    `json:"last_active_ip"`
	CreatedAt    time.Time  `json:"created_at"`
	LastActiveAt *time.Time `json:"last_active_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (a sessionAttrs) session(kind SessionKind, id string) Session {
	s := Session{Kind: kind, ID: id, UserID: deref(a.UserID), Client: deref(a.HumanName), IP: deref(a.LastActiveIP),
		CreatedAt: a.CreatedAt, LastActiveAt: a.LastActiveAt, FinishedAt: a.FinishedAt}
	if s.Client == "" {
		s.Client = deref(a.UserAgent)
	}
	switch kind {
	case CompatSession:
		s.Device = deref(a.DeviceID)
	case OAuth2Session:
		s.Device = scopeDevice(a.Scope)
	}
	return s
}

// scopeDevice is the Matrix device an OAuth 2.0 session's scope grants.
func scopeDevice(scope string) string {
	for _, tok := range strings.Fields(scope) {
		for _, p := range []string{"urn:matrix:client:device:", "urn:matrix:org.matrix.msc2967.client:device:"} {
			if d, ok := strings.CutPrefix(tok, p); ok {
				return d
			}
		}
	}
	return ""
}

// Sessions lists userID's active sessions: browser sessions first, because ending them first
// stops MAS from silently signing an app back in; then app and legacy sessions.
func (c *Client) Sessions(ctx context.Context, userID string) ([]Session, error) {
	var out []Session
	for _, kind := range []SessionKind{BrowserSession, OAuth2Session, CompatSession} {
		path := adminPrefix + sessionPaths[kind] + "?filter[user]=" + url.QueryEscape(userID) + "&filter[status]=active&page[first]=100"
		if err := c.list(ctx, path, func(r resource) error {
			var a sessionAttrs
			if err := json.Unmarshal(r.Attributes, &a); err != nil {
				return err
			}
			out = append(out, a.session(kind, r.ID))
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Session fetches one session.
func (c *Client) Session(ctx context.Context, kind SessionKind, id string) (Session, error) {
	p, ok := sessionPaths[kind]
	if !ok {
		return Session{}, fmt.Errorf("unknown session kind %q", kind)
	}
	r, err := c.one(ctx, adminPrefix+p+"/"+url.PathEscape(id))
	if err != nil {
		return Session{}, err
	}
	var a sessionAttrs
	if err := json.Unmarshal(r.Attributes, &a); err != nil {
		return Session{}, err
	}
	return a.session(kind, r.ID), nil
}

// FinishSession ends one session. MAS answers 400 for one that has already ended; that is
// confirmed by reading it back and reported as already, not as an error.
func (c *Client) FinishSession(ctx context.Context, kind SessionKind, id string) (already bool, err error) {
	p, ok := sessionPaths[kind]
	if !ok {
		return false, fmt.Errorf("unknown session kind %q", kind)
	}
	err = c.call(ctx, http.MethodPost, adminPrefix+p+"/"+url.PathEscape(id)+"/finish", nil, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		return false, err
	}
	if s, gerr := c.Session(ctx, kind, id); gerr != nil || s.FinishedAt == nil {
		return false, err
	}
	return true, nil
}

// Version is MAS's running version without the leading "v".
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, http.MethodGet, adminPrefix+"version", nil, &v); err != nil {
		return "", err
	}
	return strings.TrimPrefix(v.Version, "v"), nil
}

// ConsoleUsername is the MAS account the console acts as on Synapse's admin API. It has no
// password and no upstream link; the sweep leaves it alone (Plan).
const ConsoleUsername = "kymessages-console"

const (
	// consoleScope: Synapse requires the client API scope of every token; the admin scope is
	// what makes it an admin. No device scope: Synapse does not need one.
	consoleScope      = "urn:matrix:client:api:* urn:synapse:admin:*"
	consoleSessionTTL = 5 * time.Minute
)

// ConsoleCallTimeout bounds each detached session call AsConsole makes: the mint and the revoke.
const ConsoleCallTimeout = 10 * time.Second

// EnsureConsoleUser returns the console account's MAS ID, creating it when needed. It refuses
// an account that is locked, deactivated, linked to an upstream identity (a person's) or MAS
// admin: personal sessions need no admin flag, and with it an interactive login as the
// account could request urn:mas:admin.
func (c *Client) EnsureConsoleUser(ctx context.Context) (string, error) {
	select {
	case c.consoleMu <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-c.consoleMu }()
	path := adminPrefix + "users/by-username/" + ConsoleUsername
	r, err := c.one(ctx, path)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		var doc struct {
			Data resource `json:"data"`
		}
		err = c.call(ctx, http.MethodPost, adminPrefix+"users", []byte(`{"username":"`+ConsoleUsername+`"}`), &doc)
		r = doc.Data
		if errors.As(err, &se) && se.Status == http.StatusConflict {
			r, err = c.one(ctx, path) // created meanwhile
		}
	}
	if err != nil {
		return "", fmt.Errorf("console account: %w", err)
	}
	var a userAttrs
	if err := json.Unmarshal(r.Attributes, &a); err != nil {
		return "", fmt.Errorf("console account: %w", err)
	}
	switch {
	case a.Username != ConsoleUsername:
		return "", errors.New("console account: MAS answered with another user")
	case a.DeactivatedAt != nil:
		return "", errors.New("console account is deactivated in MAS")
	case a.LockedAt != nil:
		return "", errors.New("console account is locked in MAS")
	case a.Admin:
		return "", errors.New("console account has MAS admin; remove it (it needs none)")
	}
	var links struct {
		Data []resource `json:"data"`
	}
	if err := c.call(ctx, http.MethodGet, adminPrefix+"upstream-oauth-links?filter[user]="+url.QueryEscape(r.ID)+"&page[first]=1", nil, &links); err != nil {
		return "", fmt.Errorf("console account: %w", err)
	}
	if len(links.Data) > 0 {
		return "", errors.New("console account is linked to an upstream identity; refusing to act as a person")
	}
	return r.ID, nil
}

// AsConsole runs fn with a fresh 5-minute console session and revokes the session afterwards,
// also when fn fails or ctx ends. A failed revoke is logged; the session then expires itself.
func (c *Client) AsConsole(ctx context.Context, fn func(ctx context.Context, token string) error) error {
	userID, err := c.EnsureConsoleUser(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"actor_user_id": userID, "human_name": "KyMessages console",
		"scope": consoleScope, "expires_in": int(consoleSessionTTL / time.Second)})
	if err != nil {
		return err
	}
	var doc struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				AccessToken string `json:"access_token"`
			} `json:"attributes"`
		} `json:"data"`
	}
	// Detached: MAS may create the session even if ctx ends mid-request, and only its ID
	// lets us revoke it.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ConsoleCallTimeout)
	defer cancel()
	if err := c.call(mctx, http.MethodPost, adminPrefix+"personal-sessions", body, &doc); err != nil {
		return fmt.Errorf("console session: %w", err)
	}
	if doc.Data.ID == "" {
		return errors.New("console session: MAS returned no session ID")
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ConsoleCallTimeout)
		defer cancel()
		err := c.call(rctx, http.MethodPost, adminPrefix+"personal-sessions/"+url.PathEscape(doc.Data.ID)+"/revoke", nil, nil)
		var se *StatusError
		if err != nil && !(errors.As(err, &se) && se.Status == http.StatusConflict) {
			log.Printf("[MATRIX] console session %s not revoked; it expires within %s: %v", doc.Data.ID, consoleSessionTTL, err)
		}
	}()
	if doc.Data.Attributes.AccessToken == "" {
		return errors.New("console session: MAS returned no token")
	}
	return fn(ctx, doc.Data.Attributes.AccessToken)
}
