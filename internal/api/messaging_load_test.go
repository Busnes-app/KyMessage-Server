package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

// Opt-in transport acceptance, not MLS or browser performance evidence. Accounts
// and suite sessions are synthetic; enrollment, room ACLs, HTTP and sockets are real.
// Run the compiled test in a 2-CPU/2-GiB container; see docs/MESSAGING-LOAD.md.
func TestMessagingTransportLoad(t *testing.T) {
	if os.Getenv("KY_MESSAGING_LOAD") != "1" {
		t.Skip("opt-in constrained-host transport check")
	}
	const accounts, devices, sustained, burst = 50, 100, 600, 100
	const total = sustained + burst
	srv, st, cfg := setupTestServer(t)
	if cfg.Database.Driver != "sqlite" {
		t.Fatal("this acceptance profile requires SQLite")
	}
	type peer struct{ session, token, id string }
	peers := make([]peer, 0, devices)
	for i := range accounts {
		session := messagingLogin(t, st, fmt.Sprintf("load-%d", i))
		first := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
		second := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
		messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/devices/"+second.ID+"/approve", session, first.Token, nil), 200)
		peers = append(peers, peer{session, first.Token, first.ID}, peer{session, second.Token, second.ID})
	}
	owner := peers[0]
	created := messagingRequest(t, srv, "POST", "/api/messaging/rooms", owner.session, owner.token, map[string]string{"name": "Synthetic transport load"})
	messagingCode(t, created, 201)
	var room struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	path := "/api/messaging/rooms/" + room.ID
	for i := 1; i < accounts; i++ {
		messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", owner.session, owner.token, map[string]string{"user_id": fmt.Sprintf("load-%d", i)}), 200)
		messagingCode(t, messagingRequest(t, srv, "POST", path+"/join", peers[i*2].session, peers[i*2].token, nil), 200)
	}
	state := messagingRequest(t, srv, "GET", path+"/delivery", owner.session, owner.token, nil)
	messagingCode(t, state, 200)
	var roster struct {
		Hash string `json:"roster_hash"`
	}
	if err := json.Unmarshal(state.Body.Bytes(), &roster); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 1024))
	welcomes := make(map[string]string, devices-1)
	for _, p := range peers[1:] {
		welcomes[p.id] = payload
	}
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/events", owner.session, owner.token, map[string]any{"id": uuid.NewString(), "kind": "commit", "epoch": 0, "roster_hash": roster.Hash, "payload": payload, "welcomes": welcomes}), 200)

	server := httptest.NewServer(srv)
	t.Cleanup(func() { srv.StopMessaging(); srv.WaitDetached(); server.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	transport := &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 256}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	request := func(p peer, method, route string, body any, result any) error {
		wire, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r, err := http.NewRequestWithContext(ctx, method, server.URL+route, bytes.NewReader(wire))
		if err != nil {
			return err
		}
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: p.session})
		r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "synthetic-load-csrf"})
		r.Header.Set(auth.HeaderCSRF, "synthetic-load-csrf")
		r.Header.Set("Origin", cfg.Server.AppURL)
		r.Header.Set("X-KyMessages-Device", p.token)
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("%s %s: HTTP %d", method, route, response.StatusCode)
		}
		if result != nil {
			return json.NewDecoder(response.Body).Decode(result)
		}
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	type received struct {
		times []time.Time
		err   error
	}
	results := make(chan received, devices)
	for _, p := range peers {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path+"/live", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {cfg.Server.AppURL}, "Cookie": {auth.SessionCookieName + "=" + p.session}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		if err := c.Write(ctx, websocket.MessageText, []byte(p.token)); err != nil {
			t.Fatal(err)
		}
		if notice := readLive(t, c); notice.Sequence != 1 {
			t.Fatal("initial sequence", notice.Sequence)
		}
		go func() {
			// Each receiver owns its cursor and observations. Merge after completion.
			result := received{times: make([]time.Time, 0, total)}
			defer func() {
				if result.err != nil {
					cancel()
				}
				results <- result
			}()
			cursor := int64(1)
			for len(result.times) < total {
				var notice liveNotice
				if result.err = wsjson.Read(ctx, c, &notice); result.err != nil {
					return
				}
				for cursor < notice.Sequence {
					// Match the cookie client's account recheck before each cursor read.
					if result.err = request(p, "GET", "/api/auth/me", nil, nil); result.err != nil {
						return
					}
					var page struct {
						Next   int64
						Events []struct {
							Sequence int64
							Payload  string
						}
					}
					if result.err = request(p, "GET", fmt.Sprintf("%s/events?after=%d", path, cursor), nil, &page); result.err != nil {
						return
					}
					if len(page.Events) == 0 {
						result.err = fmt.Errorf("empty page before notified cursor")
						return
					}
					at := time.Now()
					for _, event := range page.Events {
						if event.Sequence != cursor+1 || event.Payload != payload {
							result.err = fmt.Errorf("delivery mismatch at %d", cursor)
							return
						}
						cursor = event.Sequence
						result.times = append(result.times, at)
					}
					if page.Next != cursor {
						result.err = fmt.Errorf("response cursor mismatch")
						return
					}
				}
			}
		}()
	}
	type sendJob struct {
		index     int
		scheduled time.Time
	}
	type sent struct {
		job          sendJob
		sequence     int64
		acknowledged time.Time
	}
	type senderResult struct {
		sends []sent
		err   error
	}
	jobs := make([]chan sendJob, accounts)
	senders := make(chan senderResult, accounts)
	for account := range accounts {
		jobs[account] = make(chan sendJob, 1)
		go func() {
			result := senderResult{}
			defer func() {
				if result.err != nil {
					cancel()
				}
				senders <- result
			}()
			for job := range jobs[account] {
				var receipt struct{ Sequence int64 }
				result.err = request(peers[account*2], "POST", path+"/events", map[string]any{"id": uuid.NewString(), "kind": "application", "epoch": 1, "roster_hash": roster.Hash, "payload": payload}, &receipt)
				if result.err != nil {
					return
				}
				result.sends = append(result.sends, sent{job, receipt.Sequence, time.Now()})
			}
		}()
	}
	// Accounts own independent serial senders. A global wait-for-ACK loop would
	// throttle the offered burst before the server could see concurrent senders.
	deadline := time.Now()
produce:
	for i := range total {
		interval := 100 * time.Millisecond
		if i >= sustained {
			interval = 20 * time.Millisecond
		}
		deadline = deadline.Add(interval)
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			timer.Stop()
			break produce
		case <-timer.C:
		}
		select {
		case jobs[i%accounts] <- sendJob{i, deadline}:
		case <-ctx.Done():
			break produce
		}
	}
	for _, queue := range jobs {
		close(queue)
	}
	// Merge only after each sender releases its owned observations. Sequence can
	// differ from launch order; match the server receipt rather than assuming it.
	started := make([]time.Time, total)
	phaseBySequence := make([]int, total)
	accepted := make([]time.Duration, total)
	count := 0
	var sendError error
	for range accounts {
		result := <-senders
		if result.err != nil {
			sendError = result.err
		}
		for _, send := range result.sends {
			if send.sequence < 2 || send.sequence > total+1 || !started[send.sequence-2].IsZero() {
				sendError = fmt.Errorf("invalid/duplicate append sequence")
				cancel()
				continue
			}
			started[send.sequence-2] = send.job.scheduled
			if send.job.index >= sustained {
				phaseBySequence[send.sequence-2] = 1
			}
			// Include scheduling backlog, not just successful request RTT.
			accepted[send.job.index] = send.acknowledged.Sub(send.job.scheduled)
			count++
		}
	}
	if count != total && sendError == nil {
		sendError = fmt.Errorf("incomplete offered workload")
		cancel()
	}
	latencies := [2][]time.Duration{make([]time.Duration, 0, devices*sustained), make([]time.Duration, 0, devices*burst)}
	var firstError error
	for range devices {
		result := <-results
		if result.err != nil {
			if firstError == nil || strings.Contains(result.err.Error(), "HTTP ") {
				firstError = result.err
			}
			continue
		}
		for i, at := range result.times {
			phase := phaseBySequence[i]
			latencies[phase] = append(latencies[phase], at.Sub(started[i]))
		}
	}
	if sendError != nil || firstError != nil {
		t.Fatalf("accepted=%d/%d send=%v receive=%v", count, total, sendError, firstError)
	}
	p95 := func(values []time.Duration) time.Duration {
		slices.Sort(values)
		return values[(len(values)*95+99)/100-1]
	}
	t.Logf("50 accounts, 100 devices; 1024-byte opaque payload; %d receipts, %d deliveries", len(accepted), len(latencies[0])+len(latencies[1]))
	for phase, timings := range [][]time.Duration{accepted[:sustained], accepted[sustained:]} {
		accept95, receive95 := p95(timings), p95(latencies[phase])
		label := "sustained 60s at 10/s"
		if phase == 1 {
			label = "burst 2s at 50/s"
		}
		t.Logf("%s: p95 accept=%s receipt=%s", label, accept95, receive95)
		if accept95 >= 250*time.Millisecond || receive95 >= time.Second {
			t.Errorf("%s transport latency exceeds planning targets", label)
		}
	}
}
