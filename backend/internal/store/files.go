package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

func (s *Store) CreateFile(ctx context.Context, ownerID, kind, name, mime, sha, path string, size int64, pages, retentionDays int) (*StoredFile, error) {
	f := &StoredFile{
		ID:           "f_" + uuid.NewString(),
		OwnerID:      ownerID,
		Kind:         kind,
		OriginalName: name,
		MIME:         mime,
		SizeBytes:    size,
		SHA256:       sha,
		StoragePath:  path,
		PageCount:    pages,
		CreatedAt:    Now(),
	}
	if retentionDays > 0 {
		f.ExpiresAt = time.Now().UTC().Add(time.Duration(retentionDays) * 24 * time.Hour).Format(time.RFC3339)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO files
		(id, owner_id, kind, original_name, mime, size_bytes, sha256, storage_path, page_count, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)`,
		f.ID, f.OwnerID, f.Kind, f.OriginalName, f.MIME, f.SizeBytes, f.SHA256,
		f.StoragePath, f.PageCount, f.ExpiresAt, f.CreatedAt)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (s *Store) GetFile(ctx context.Context, id string) (*StoredFile, error) {
	var f StoredFile
	row := s.DB.QueryRowContext(ctx, `SELECT id, owner_id, kind, original_name, mime,
		size_bytes, sha256, storage_path, page_count, COALESCE(expires_at, ''), created_at
		FROM files WHERE id = ?`, id)
	err := row.Scan(&f.ID, &f.OwnerID, &f.Kind, &f.OriginalName, &f.MIME,
		&f.SizeBytes, &f.SHA256, &f.StoragePath, &f.PageCount, &f.ExpiresAt, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *Store) DeleteFile(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
	return err
}

// PathUsage counts rows that still retain the bytes and active jobs that need
// them. Expired rows sharing the path do not retain it.
func (s *Store) PathUsage(ctx context.Context, path string) (retained, active int, err error) {
	if path == "" {
		return 0, 0, nil
	}
	now := Now()
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM files
		WHERE storage_path = ? AND (expires_at IS NULL OR expires_at >= ?)`,
		path, now).Scan(&retained); err != nil {
		return 0, 0, err
	}
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs j
		JOIN files f ON f.id = j.original_file_id OR f.id = j.pdf_file_id
		WHERE f.storage_path = ? AND j.status IN ('queued','claimed','printing')`,
		path).Scan(&active)
	return retained, active, err
}

// DetachExpiredFile clears one expired row's pointer without deleting bytes
// that another retained row still shares.
func (s *Store) DetachExpiredFile(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE files SET storage_path = ''
		WHERE id = ? AND storage_path <> ''`, id)
	return err
}

// PurgePath drops the stored bytes reference but keeps metadata rows, because
// print job history and its foreign keys still reference the files.
func (s *Store) PurgePath(ctx context.Context, path string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE files SET storage_path = '' WHERE storage_path = ?`, path)
	return err
}

func (s *Store) ExpiredFiles(ctx context.Context, limit int) ([]StoredFile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, owner_id, kind, original_name, mime,
		size_bytes, sha256, storage_path, page_count, COALESCE(expires_at, ''), created_at
		FROM files WHERE expires_at IS NOT NULL AND expires_at < ?
		AND storage_path <> ''
		AND NOT EXISTS (SELECT 1 FROM print_jobs j
			WHERE (j.original_file_id = files.id OR j.pdf_file_id = files.id)
			AND j.status IN ('queued','claimed','printing'))
		LIMIT ?`, Now(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredFile{}
	for rows.Next() {
		var f StoredFile
		if err := rows.Scan(&f.ID, &f.OwnerID, &f.Kind, &f.OriginalName, &f.MIME,
			&f.SizeBytes, &f.SHA256, &f.StoragePath, &f.PageCount, &f.ExpiresAt, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RetainedFileIDs maps every file whose bytes are still on disk to true. The
// cleanup loop uses it to find preview images whose upload is already gone:
// those can never be served again (authorizedFile refuses a cleared path), so
// leaving them behind only fills the NAS.
func (s *Store) RetainedFileIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM files WHERE storage_path <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// RetainedStoragePaths returns every storage path a retained row still points
// at. Deleting a user cascades the files rows away but leaves the bytes on
// disk, so those paths are exactly what the orphan sweep may reclaim.
func (s *Store) RetainedStoragePaths(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT storage_path FROM files WHERE storage_path <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		out[path] = true
	}
	return out, rows.Err()
}
