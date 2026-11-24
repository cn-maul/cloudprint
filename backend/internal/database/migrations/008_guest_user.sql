-- A built-in principal for unauthenticated visitors. They may list printers,
-- upload and print; everything under /api/admin stays behind adminOnly, so a
-- guest can never add or remove a printer.
--
-- Keeping guests as a real users row (instead of a nil user) means jobs, files,
-- quota and the audit log keep their owner columns filled without special
-- cases downstream.
ALTER TABLE users ADD COLUMN is_guest INTEGER NOT NULL DEFAULT 0;

INSERT INTO users
    (id, username, display_name, password_hash, role, daily_page_quota,
     is_active, must_change_password, is_guest, created_at)
VALUES
    ('u_guest', '__guest__', '访客', '!', 'user', 0, 1, 0, 1, '1970-01-01T00:00:00Z')
ON CONFLICT(id) DO NOTHING;
