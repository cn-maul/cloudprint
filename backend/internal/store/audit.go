package store

import "context"

func (s *Store) Audit(ctx context.Context, actorID, action, targetType, targetID string, detail any) {
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO audit_logs
		(actor_id, action, target_type, target_id, detail_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		actorID, action, targetType, targetID, jsonString(detail), Now())
}
