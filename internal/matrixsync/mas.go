package matrixsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const adminPrefix = "/api/admin/v1/"

// Client calls MAS's admin API with a client-credentials token, cached until shortly before
// it expires and refetched once on a 401.
type Client struct {
	base, id, secret string
	hc               *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewClient(baseURL, clientID, secret string) *Client {
	return &Client{base: strings.TrimSuffix(baseURL, "/"), id: clientID, secret: secret, hc: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

func (c *Client) dropToken() { c.mu.Lock(); c.token = ""; c.mu.Unlock() }

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
			c.dropToken()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("MAS %s %s: HTTP %d", method, path, resp.StatusCode)
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
	if err := c.list(ctx, adminPrefix+"upstream-oauth-links?filter[provider]="+url.QueryEscape(providers[0])+"&page[first]=100", func(r resource) error {
		var a struct {
			Subject string  `json:"subject"`
			UserID  *string `json:"user_id"`
		}
		if err := json.Unmarshal(r.Attributes, &a); err != nil {
			return err
		}
		if a.UserID != nil {
			subjects[*a.UserID] = a.Subject
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var users []User
	err := c.list(ctx, adminPrefix+"users?page[first]=100", func(r resource) error {
		var a struct {
			Username      string  `json:"username"`
			LockedAt      *string `json:"locked_at"`
			DeactivatedAt *string `json:"deactivated_at"`
		}
		if err := json.Unmarshal(r.Attributes, &a); err != nil {
			return err
		}
		users = append(users, User{ID: r.ID, Username: a.Username, Subject: subjects[r.ID],
			Locked: a.LockedAt != nil, Deactivated: a.DeactivatedAt != nil})
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
