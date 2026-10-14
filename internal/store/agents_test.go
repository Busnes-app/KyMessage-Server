package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

func TestAgentStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t).Agents()
	a := &store.Agent{ID: "agt_1", Username: "agent-helper", MASUserID: "01MAS1", DeviceID: "DEVICE0000000001",
		OwnerSubject: "kid-alice", SessionID: "01SESS1", TokenState: store.AgentTokenActive, CreatedBy: "admin"}
	if err := st.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAgent(ctx, "agt_1")
	if err != nil || got.Username != a.Username || got.SessionID != "01SESS1" || got.OwnerSubject != "kid-alice" || got.CreatedAt.IsZero() {
		t.Fatalf("GetAgent: %+v %v", got, err)
	}

	for _, dup := range []store.Agent{
		{ID: "agt_2", Username: "agent-helper", MASUserID: "01MAS2", DeviceID: "DEVICE0000000002"},
		{ID: "agt_2", Username: "agent-other", MASUserID: "01MAS1", DeviceID: "DEVICE0000000002"},
		{ID: "agt_2", Username: "agent-other", MASUserID: "01MAS2", DeviceID: "DEVICE0000000001"},
	} {
		dup.OwnerSubject, dup.SessionID, dup.TokenState, dup.CreatedBy = "kid-bob", "01SESS2", store.AgentTokenActive, "admin"
		if err := st.CreateAgent(ctx, &dup); !errors.Is(err, store.ErrAlreadyExists) {
			t.Fatalf("duplicate %+v: %v", dup, err)
		}
	}

	if err := st.SetAgentToken(ctx, "agt_1", "DEVICE0000000001", "", store.AgentTokenExpired); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetAgent(ctx, "agt_1"); got.SessionID != "" || got.TokenState != store.AgentTokenExpired {
		t.Fatalf("after expiry: %+v", got)
	}
	if err := st.SetAgentToken(ctx, "agt_1", "DEVICE0000000009", "01SESS3", store.AgentTokenActive); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentToken(ctx, "missing", "D", "", store.AgentTokenRevoked); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetAgentToken missing: %v", err)
	}

	list, err := st.ListAgents(ctx)
	if err != nil || len(list) != 1 || list[0].SessionID != "01SESS3" || list[0].DeviceID != "DEVICE0000000009" {
		t.Fatalf("ListAgents: %+v %v", list, err)
	}
	if err := st.DeleteAgent(ctx, "agt_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAgent(ctx, "agt_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAgent after delete: %v", err)
	}
	if err := st.DeleteAgent(ctx, "agt_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteAgent twice: %v", err)
	}
}

// An active token needs its session; an ended one must not keep one.
func TestAgentTokenStateIsConsistent(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t).Agents()
	bad := []struct{ session, state string }{
		{"", store.AgentTokenActive},
		{"01SESS", store.AgentTokenRevoked},
		{"01SESS", store.AgentTokenExpired},
		{"01SESS", "bogus"},
	}
	for _, b := range bad {
		a := &store.Agent{ID: "agt", Username: "agent-x", MASUserID: "m", DeviceID: "d", OwnerSubject: "s", SessionID: b.session, TokenState: b.state, CreatedBy: "admin"}
		if err := st.CreateAgent(ctx, a); err == nil {
			t.Fatalf("CreateAgent accepted %+v", b)
		}
	}
	a := &store.Agent{ID: "agt", Username: "agent-x", MASUserID: "m", DeviceID: "d", OwnerSubject: "s", SessionID: "01SESS", TokenState: store.AgentTokenActive, CreatedBy: "admin"}
	if err := st.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		if err := st.SetAgentToken(ctx, "agt", "d", b.session, b.state); err == nil {
			t.Fatalf("SetAgentToken accepted %+v", b)
		}
	}
}
