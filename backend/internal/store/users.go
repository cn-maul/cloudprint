package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

type Store struct {
	DB *sql.DB
}

func New(db *sql.DB) *Store { return &Store{DB: db} }

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	// The built-in guest row exists from migration 008 onward. It must not
	// count as "someone already set this install up", or a fresh database
	// would skip bootstrap and have no administrator to log in with.
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_guest = 0`).Scan(&n)
	return n, err
}

// GuestUserID is the fixed row created by migration 008. A constant rather
// than a lookup keeps the unauthenticated path free of a database round-trip
// on every request.
const GuestUserID = "u_guest"

// GuestUser returns the built-in visitor principal. It has no usable password
// and role "user", so /api/admin refuses it and quota stays at 0 (unlimited).
func (s *Store) GuestUser(ctx context.Context) (*User, error) {
	return s.GetUser(ctx, GuestUserID)
}

func (s *Store) CreateUser(ctx context.Context, username, displayName, passwordHash, role string, quota int) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 64 {
		return nil, fmt.Errorf("用户名长度必须为 1-64 个字符")
	}
	if role != "admin" && role != "user" {
		role = "user"
	}
	if quota < 0 {
		quota = 0
	}
	u := &User{
		ID:             "u_" + uuid.NewString(),
		Username:       username,
		DisplayName:    strings.TrimSpace(displayName),
		PasswordHash:   passwordHash,
		Role:           role,
		DailyPageQuota: quota,
		IsActive:       true,
		CreatedAt:      Now(),
	}
	if u.DisplayName == "" {
		u.DisplayName = username
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO users
		(id, username, display_name, password_hash, role, daily_page_quota, is_active, must_change_password, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, 0, ?)`,
		u.ID, u.Username, u.DisplayName, u.PasswordHash, u.Role, u.DailyPageQuota, u.CreatedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrConflict
		}
		return nil, err
	}
	return u, nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT id, username, display_name, password_hash, role,
		daily_page_quota, is_active, must_change_password, is_guest, created_at
		FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT id, username, display_name, password_hash, role,
		daily_page_quota, is_active, must_change_password, is_guest, created_at
		FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role,
		&u.DailyPageQuota, &u.IsActive, &u.MustChangePassword, &u.IsGuest, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, username, display_name, '' AS password_hash, role,
		daily_page_quota, is_active, must_change_password, is_guest, created_at
		FROM users WHERE is_guest = 0 ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role,
			&u.DailyPageQuota, &u.IsActive, &u.MustChangePassword, &u.IsGuest, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UpdateUser(ctx context.Context, id string, displayName *string, role *string, quota *int, active *bool) error {
	if displayName != nil {
		if _, err := s.DB.ExecContext(ctx, `UPDATE users SET display_name = ? WHERE id = ?`, *displayName, id); err != nil {
			return err
		}
	}
	if role != nil {
		if *role != "admin" && *role != "user" {
			return fmt.Errorf("无效角色")
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, *role, id); err != nil {
			return err
		}
	}
	if quota != nil {
		if _, err := s.DB.ExecContext(ctx, `UPDATE users SET daily_page_quota = ? WHERE id = ?`, *quota, id); err != nil {
			return err
		}
	}
	if active != nil {
		if _, err := s.DB.ExecContext(ctx, `UPDATE users SET is_active = ? WHERE id = ?`, boolInt(*active), id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteUser removes a local account. Sessions, files and print jobs are
// cascaded by the schema; group memberships are removed explicitly for older
// databases. The last administrator cannot be deleted.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.IsGuest {
		return fmt.Errorf("访客账号是内置的，不能删除")
	}
	if u.Role == "admin" {
		var admins int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND is_active = 1`).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return fmt.Errorf("不能删除最后一个管理员账号")
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_members WHERE user_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) UpdateDisplayName(ctx context.Context, id, displayName string) error {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len([]rune(displayName)) > 80 {
		return fmt.Errorf("显示名长度必须为 1-80 个字符")
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE users SET display_name = ? WHERE id = ?`, displayName, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetPassword(ctx context.Context, id, hash string, mustChange bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = ? WHERE id = ?`,
		hash, boolInt(mustChange), id)
	return err
}

type Session struct {
	ID        string
	UserID    string
	CSRF      string
	ExpiresAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, userID, sessionID, csrf string, hours int, ua, ip string) error {
	now := time.Now().UTC()
	exp := now.Add(time.Duration(hours) * time.Hour)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions (id, user_id, csrf_token, created_at, expires_at, user_agent, ip)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, userID, csrf, now.Format(time.RFC3339),
		exp.Format(time.RFC3339), truncate(ua, 300), truncate(ip, 80))
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (*Session, *User, error) {
	var se Session
	var expires string
	row := s.DB.QueryRowContext(ctx, `SELECT id, user_id, csrf_token, expires_at FROM sessions WHERE id = ?`, id)
	if err := row.Scan(&se.ID, &se.UserID, &se.CSRF, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	se.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	if time.Now().UTC().After(se.ExpiresAt) {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		return nil, nil, ErrNotFound
	}
	u, err := s.GetUser(ctx, se.UserID)
	if err != nil {
		return nil, nil, err
	}
	if !u.IsActive {
		return nil, nil, ErrNotFound
	}
	return &se, u, nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *Store) CleanupSessions(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, Now())
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func NullString(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}
