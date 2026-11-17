package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

func (s *Store) CreateAgent(ctx context.Context, name, tokenHash, printCommand string) (*Agent, error) {
	a := &Agent{
		ID:           "a_" + uuid.NewString(),
		Name:         name,
		PrintCommand: printCommand,
		IsActive:     true,
		CreatedAt:    Now(),
		Queues:       []any{},
		Status:       map[string]any{},
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agents
		(id, name, token_hash, print_command, queues_json, status_json, is_active, created_at)
		VALUES (?, ?, ?, ?, '[]', '{}', 1, ?)`, a.ID, a.Name, tokenHash, printCommand, a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Store) GetAgentByToken(ctx context.Context, hash string) (*Agent, error) {
	var a Agent
	var queues, status string
	var lastSeen sql.NullString
	row := s.DB.QueryRowContext(ctx, `SELECT id, name, version, print_command, COALESCE(last_seen_at, ''),
		queues_json, status_json, is_active, created_at FROM agents WHERE token_hash = ?`, hash)
	err := row.Scan(&a.ID, &a.Name, &a.Version, &a.PrintCommand, &lastSeen,
		&queues, &status, &a.IsActive, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.LastSeenAt = lastSeen.String
	_ = jsonUnmarshal(queues, &a.Queues)
	_ = jsonUnmarshal(status, &a.Status)
	return &a, nil
}

func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, version, print_command,
		COALESCE(last_seen_at, ''), queues_json, status_json, is_active, created_at
		FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		var a Agent
		var queues, status string
		if err := rows.Scan(&a.ID, &a.Name, &a.Version, &a.PrintCommand, &a.LastSeenAt,
			&queues, &status, &a.IsActive, &a.CreatedAt); err != nil {
			return nil, err
		}
		_ = jsonUnmarshal(queues, &a.Queues)
		_ = jsonUnmarshal(status, &a.Status)
		if a.LastSeenAt != "" {
			t, _ := time.Parse(time.RFC3339, a.LastSeenAt)
			a.Online = time.Since(t) < 2*time.Minute
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAgent(ctx context.Context, id string) (*Agent, error) {
	list, err := s.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) HeartbeatAgent(ctx context.Context, id, version string, queues, status any) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE agents SET version = ?, last_seen_at = ?,
		queues_json = ?, status_json = ? WHERE id = ?`,
		version, Now(), jsonString(queues), jsonString(status), id)
	return err
}

func (s *Store) UpdateAgentToken(ctx context.Context, id, tokenHash string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE agents SET token_hash = ? WHERE id = ?`, tokenHash, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	return err
}

// DeactivateAgent retires a helper without deleting its row. Historic jobs and
// endpoints reference the agent, and the token must stop working immediately.
func (s *Store) DeactivateAgent(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE agents SET is_active = 0, last_seen_at = NULL WHERE id = ?`, id)
	return err
}

func (s *Store) ActiveEndpointForAgent(ctx context.Context, agentID string) (*Endpoint, error) {
	row := s.DB.QueryRowContext(ctx, endpointSelect+` WHERE agent_id = ? AND is_enabled = 1`,
		agentID)
	ep, err := scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ep, nil
}
