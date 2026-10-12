package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// blockedPeerRanges is Synapse 1.162's DEFAULT_IP_RANGE_BLOCKLIST (synapse/config/server.py),
// so the app refuses exactly the addresses Synapse refuses for federation.
var blockedPeerRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
		"192.0.0.0/24", "169.254.0.0/16", "192.88.99.0/24", "198.18.0.0/15", "192.0.2.0/24", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "::1/128", "fe80::/10", "fc00::/7", "2001:db8::/32", "ff00::/8", "fec0::/10",
		"0.0.0.0/8", "::/128", "::ffff:0:0/96"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// publicOnly refuses a dial to any blocked address after DNS resolution, so a server's
// well-known cannot point the app at the internal network.
func publicOnly(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	ip := ap.Addr().Unmap()
	for _, p := range blockedPeerRanges {
		if p.Contains(ip) {
			return fmt.Errorf("refusing to connect to non-public address %s", ip)
		}
	}
	return nil
}

// newFederationHTTP is the app's only client for other homeservers: public addresses only,
// no proxy, no redirects, verified TLS.
func newFederationHTTP() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: publicOnly}
	return &http.Client{
		Timeout:       8 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// federationBase finds where a server takes federation requests: its
// /.well-known/matrix/server delegation, else <name>:8448. SRV records are not consulted.
func (s *Server) federationBase(ctx context.Context, name string) (string, error) {
	if s.fedBaseForTest != nil {
		return s.fedBaseForTest(name), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+name+"/.well-known/matrix/server", nil)
	if err != nil {
		return "", err
	}
	host := name + ":8448"
	if resp, err := s.fedHTTP.Do(req); err == nil {
		var wk struct {
			Server string `json:"m.server"`
		}
		if resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&wk) == nil && wk.Server != "" {
			host = wk.Server
		}
		resp.Body.Close()
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.Hostname() == "" {
		return "", errors.New("unusable federation delegation")
	}
	if u.Port() == "" {
		host += ":8448"
	}
	return "https://" + host, nil
}

// openIDUser verifies a Matrix OpenID token with the server that issued it: our Synapse on the
// internal network, or an allowed server through the public-only client. It returns the
// token's user, which must belong to that server.
func (s *Server) openIDUser(ctx context.Context, serverName, token string) (string, int, error) {
	client, base := s.probeHTTP, s.matrixTargets.Synapse
	if serverName != s.config.Matrix.ServerName {
		var err error
		if base, err = s.federationBase(ctx, serverName); err != nil {
			return "", 502, err
		}
		client = s.fedHTTP
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/_matrix/federation/v1/openid/userinfo?access_token="+url.QueryEscape(token), nil)
	if err != nil {
		return "", 502, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 502, err
	}
	defer resp.Body.Close()
	var user struct {
		Sub string `json:"sub"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&user) != nil ||
		!strings.HasPrefix(user.Sub, "@") || mxidServer(user.Sub) != serverName {
		return "", 401, errors.New("invalid Matrix OpenID token")
	}
	return user.Sub, 200, nil
}

// mxidServer is the server part of @local:server (a localpart never contains ':').
func mxidServer(mxid string) string {
	_, server, _ := strings.Cut(mxid, ":")
	return server
}
