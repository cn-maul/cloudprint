package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT g.id, g.name, g.description, g.created_at, g.updated_at,
		(SELECT COUNT(*) FROM user_group_members m WHERE m.group_id = g.id)
		FROM user_groups g ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount); err != nil {
			return nil, err
		}
		g.PrinterIDs = []string{}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		printers, err := s.groupPrinterIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].PrinterIDs = printers
		members, err := s.GroupMembers(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Members = members
	}
	return out, nil
}

func (s *Store) groupPrinterIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT printer_id FROM user_group_printers WHERE group_id = ?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) GetGroup(ctx context.Context, id string) (*Group, error) {
	var g Group
	row := s.DB.QueryRowContext(ctx, `SELECT g.id, g.name, g.description, g.created_at, g.updated_at,
		(SELECT COUNT(*) FROM user_group_members m WHERE m.group_id = g.id)
		FROM user_groups g WHERE g.id = ?`, id)
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.PrinterIDs, _ = s.groupPrinterIDs(ctx, id)
	members, err := s.GroupMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	g.Members = members
	return &g, nil
}

func (s *Store) CreateGroup(ctx context.Context, name, description string) (*Group, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return nil, fmt.Errorf("分组名称长度必须为 1-80 个字符")
	}
	now := Now()
	g := &Group{
		ID:          "g_" + uuid.NewString(),
		Name:        name,
		Description: strings.TrimSpace(description),
		PrinterIDs:  []string{},
		Members:     []GroupUser{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_groups(id, name, description, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?)`, g.ID, g.Name, g.Description, g.CreatedAt, g.UpdatedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrConflict
		}
		return nil, err
	}
	return g, nil
}

func (s *Store) UpdateGroup(ctx context.Context, id string, name, description *string) error {
	sets := []string{}
	args := []any{}
	if name != nil {
		v := strings.TrimSpace(*name)
		if v == "" {
			return fmt.Errorf("分组名称不能为空")
		}
		sets = append(sets, "name = ?")
		args = append(args, v)
	}
	if description != nil {
		sets = append(sets, "description = ?")
		args = append(args, strings.TrimSpace(*description))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, Now(), id)
	res, err := s.DB.ExecContext(ctx, `UPDATE user_groups SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrConflict
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM user_groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GroupMembers(ctx context.Context, groupID string) ([]GroupUser, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id, u.username, u.display_name, u.role, u.is_active
		FROM user_group_members m JOIN users u ON u.id = m.user_id
		WHERE m.group_id = ? ORDER BY u.username`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupUser{}
	for rows.Next() {
		var u GroupUser
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.IsActive); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) SetGroupMembers(ctx context.Context, groupID string, userIDs []string) error {
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_members WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	now := Now()
	seen := map[string]bool{}
	for _, uid := range userIDs {
		uid = strings.TrimSpace(uid)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_group_members(group_id, user_id, created_at)
			SELECT ?, id, ? FROM users WHERE id = ?`, groupID, now, uid); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_groups SET updated_at = ? WHERE id = ?`, now, groupID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetGroupPrinters(ctx context.Context, groupID string, printerIDs []string) error {
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_printers WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	now := Now()
	seen := map[string]bool{}
	for _, pid := range printerIDs {
		pid = strings.TrimSpace(pid)
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_group_printers(group_id, printer_id, created_at)
			SELECT ?, id, ? FROM printers WHERE id = ?`, groupID, now, pid); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_groups SET updated_at = ? WHERE id = ?`, now, groupID); err != nil {
		return err
	}
	return tx.Commit()
}

// UserCanUsePrinter returns true for admins and guests, or when any of the
// user's groups is granted the printer. This is the single authorization rule
// used by both listing and job creation.
//
// Guests are allowed every printer on purpose: they cannot add or remove one
// (that lives under /api/admin), so "guest = use what already exists" is
// exactly this rule. Groups only narrow what a real user account can reach.
func (s *Store) UserCanUsePrinter(ctx context.Context, u *User, printerID string) (bool, error) {
	if u == nil {
		return false, nil
	}
	if u.Role == "admin" || u.IsGuest {
		return true, nil
	}
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_group_members m
		JOIN user_group_printers gp ON gp.group_id = m.group_id
		WHERE m.user_id = ? AND gp.printer_id = ?`, u.ID, printerID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) UserGroupIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT group_id FROM user_group_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) PrinterGroupIDs(ctx context.Context, printerID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT group_id FROM user_group_printers WHERE printer_id = ?`, printerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
