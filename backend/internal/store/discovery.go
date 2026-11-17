package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

func (s *Store) CreateDiscoveryRun(ctx context.Context, source, cidr string) (*DiscoveryRun, error) {
	r := &DiscoveryRun{
		ID:        "d_" + uuid.NewString(),
		Source:    source,
		CIDR:      cidr,
		Status:    "running",
		StartedAt: Now(),
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO printer_discovery_runs
		(id, source, cidr, status, started_at) VALUES (?, ?, ?, 'running', ?)`,
		r.ID, r.Source, r.CIDR, r.StartedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) FinishDiscoveryRun(ctx context.Context, id string, found int, errMsg string) error {
	status := "completed"
	// A single unreachable mDNS service must not label a successful subnet
	// scan as failed. Only report failure when the search produced nothing.
	if errMsg != "" && found == 0 {
		status = "failed"
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE printer_discovery_runs SET status = ?, finished_at = ?,
		found_count = ?, error_message = ? WHERE id = ?`, status, Now(), found, truncate(errMsg, 1000), id)
	return err
}

func (s *Store) ListDiscoveryRuns(ctx context.Context, limit int) ([]DiscoveryRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, source, cidr, status, started_at,
		COALESCE(finished_at, ''), found_count, error_message FROM printer_discovery_runs
		ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscoveryRun{}
	for rows.Next() {
		var r DiscoveryRun
		if err := rows.Scan(&r.ID, &r.Source, &r.CIDR, &r.Status, &r.StartedAt,
			&r.FinishedAt, &r.FoundCount, &r.ErrorMessage); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetDiscoveryRun(ctx context.Context, id string) (*DiscoveryRun, error) {
	var r DiscoveryRun
	row := s.DB.QueryRowContext(ctx, `SELECT id, source, cidr, status, started_at,
		COALESCE(finished_at, ''), found_count, error_message FROM printer_discovery_runs WHERE id = ?`, id)
	err := row.Scan(&r.ID, &r.Source, &r.CIDR, &r.Status, &r.StartedAt, &r.FinishedAt,
		&r.FoundCount, &r.ErrorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
