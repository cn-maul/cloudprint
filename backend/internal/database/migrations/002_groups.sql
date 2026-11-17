PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS user_groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS user_group_members (
    group_id TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_group_members_user ON user_group_members(user_id);

CREATE TABLE IF NOT EXISTS user_group_printers (
    group_id TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    printer_id TEXT NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (group_id, printer_id)
);
CREATE INDEX IF NOT EXISTS idx_group_printers_printer ON user_group_printers(printer_id);
