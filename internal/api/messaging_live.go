package api

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

const messagingLiveHeartbeat = 15 * time.Second

// Each connection owns one coalescing signal. The registry lock protects only
// admission, shutdown and iteration; it never covers networking or database work.
type messagingLiveConnection struct {
	user, room string
	wake       chan struct{}
	cancel     context.CancelFunc
}
type messagingLiveRegistry struct {
	mu          sync.Mutex
	closing     bool
	connections map[*messagingLiveConnection]struct{}
}

func (s *Server) registerMessagingLive(user, room string) (*messagingLiveConnection, context.Context, bool) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if s.live.closing || len(s.live.connections) >= 256 {
		return nil, nil, false
	}
	count := 0
	for connection := range s.live.connections {
		if connection.user == user {
			count++
		}
	}
	if count >= 4 {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	connection := &messagingLiveConnection{user: user, room: room, wake: make(chan struct{}, 1), cancel: cancel}
	if s.live.connections == nil {
		s.live.connections = make(map[*messagingLiveConnection]struct{})
	}
	s.live.connections[connection] = struct{}{}
	return connection, ctx, true
}
func (s *Server) unregisterMessagingLive(connection *messagingLiveConnection) {
	connection.cancel()
	s.live.mu.Lock()
	delete(s.live.connections, connection)
	s.live.mu.Unlock()
}
func (s *Server) wakeMessaging(room string) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	for connection := range s.live.connections {
		if room == "" || connection.room == room {
			select {
			case connection.wake <- struct{}{}:
			default:
			}
		}
	}
}

// WakeMessaging signals every live stream to recheck its device and roster, for changes
// made outside a request such as the suspended-device expiry sweep.
func (s *Server) WakeMessaging() { s.wakeMessaging("") }

// StopMessaging closes upgraded connections and rejects new stream registrations.
// Call before HTTP shutdown, then WaitDetached before closing the database.
func (s *Server) StopMessaging() {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	s.live.closing = true
	for connection := range s.live.connections {
		connection.cancel()
	}
}

func (s *Server) handleMessagingLive(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	app, err := url.Parse(s.config.Server.AppURL)
	if err != nil || r.Header.Get("Origin") != app.Scheme+"://"+app.Host {
		s.writeError(w, http.StatusForbidden, "Exact application origin required")
		return
	}
	room := r.PathValue("room")
	parsed, err := uuid.Parse(room)
	if err != nil || parsed.String() != room || r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "Canonical room path without query required")
		return
	}
	subscription, ctx, ok := s.registerMessagingLive(actor.UserID, room)
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "Live connection limit or shutdown")
		return
	}
	defer s.unregisterMessagingLive(subscription)
	// The exact configured scheme+host was required above. The library's additional
	// request-Host comparison is unsuitable behind a reverse proxy; never rely on it.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64)
	credentialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	kind, credential, err := conn.Read(credentialCtx)
	cancel()
	if err != nil {
		return
	}
	if kind != websocket.MessageText || !messagingHex(string(credential)) {
		conn.Close(websocket.StatusPolicyViolation, "Invalid device credential")
		return
	}
	actor.DeviceTokenHash = crypto.SHA256Hex(credential)
	clear(credential)
	ctx = conn.CloseRead(ctx) // Extra data frames are forbidden; process control frames.
	ticker := time.NewTicker(messagingLiveHeartbeat)
	defer ticker.Stop()
	for {
		// Recheck the live suite session, device and room ACL before every signal.
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		state, err := s.store.Messaging().DeliveryState(checkCtx, actor, room)
		if err == nil {
			err = wsjson.Write(checkCtx, conn, struct {
				Kind       string `json:"kind"`
				Sequence   int64  `json:"sequence"`
				Epoch      int64  `json:"epoch"`
				RosterHash string `json:"roster_hash"`
			}{"wake", state.Sequence, state.Epoch, state.RosterHash})
		}
		cancel()
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-subscription.wake:
			// Bound burst work and merge notifications; HTTP cursor reads remain durable.
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
