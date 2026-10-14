package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type agentStore struct {
	store *SQLStore
}

const agentColumns = "id, username, mas_user_id, device_id, owner_subject, session_id, token_state, created_by, created_at"

func scanAgent(row interface{ Scan(...any) error }) (*Agent, error) {
	var a Agent
	err := row.Scan(&a.ID, &a.Username, &a.MASUserID, &a.DeviceID, &a.OwnerSubject, &a.SessionID, &a.TokenState, &a.CreatedBy, &a.CreatedAt)
	return &a, err
}

func validAgentToken(sessionID, state string) error {
	switch {
	case state == AgentTokenActive && sessionID != "":
	case (state == AgentTokenRevoked || state == AgentTokenExpired) && sessionID == "":
	default:
		return fmt.Errorf("agent token state %q with session %q", state, sessionID)
	}
	return nil
}

func (s *agentStore) CreateAgent(ctx context.Context, a *Agent) error {
	if err := validAgentToken(a.SessionID, a.TokenState); err != nil {
		return err
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	q := s.store.rebind(`INSERT INTO agents (` + agentColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := s.store.db.ExecContext(ctx, q, a.ID, a.Username, a.MASUserID, a.DeviceID, a.OwnerSubject, a.SessionID, a.TokenState, a.CreatedBy, a.CreatedAt)
	return uniqueError(err)
}

func (s *agentStore) GetAgent(ctx context.Context, id string) (*Agent, error) {
	a, err := scanAgent(s.store.db.QueryRowContext(ctx, s.store.rebind(`SELECT `+agentColumns+` FROM agents WHERE id = ?`), id))
	if errorsIs(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *agentStore) ListAgents(ctx context.Context) ([]*Agent, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+agentColumns+` FROM agents ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *agentStore) SetAgentToken(ctx context.Context, id, sessionID, state string) error {
	if err := validAgentToken(sessionID, state); err != nil {
		return err
	}
	res, err := s.store.db.ExecContext(ctx, s.store.rebind(`UPDATE agents SET session_id = ?, token_state = ? WHERE id = ?`), sessionID, state, id)
	return oneRow(res, err)
}

func (s *agentStore) DeleteAgent(ctx context.Context, id string) error {
	res, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM agents WHERE id = ?`), id)
	return oneRow(res, err)
}

func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
