PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    display_name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    daily_page_quota INTEGER NOT NULL DEFAULT 50,
    is_active INTEGER NOT NULL DEFAULT 1,
    must_change_password INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS printer_profiles (
    id TEXT PRIMARY KEY,
    vendor TEXT NOT NULL DEFAULT '',
    model_pattern TEXT NOT NULL DEFAULT '',
    transports_json TEXT NOT NULL DEFAULT '[]',
    capabilities_json TEXT NOT NULL DEFAULT '{}',
    notes TEXT NOT NULL DEFAULT '',
    is_builtin INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    token_hash TEXT NOT NULL,
    version TEXT NOT NULL DEFAULT '',
    print_command TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT,
    queues_json TEXT NOT NULL DEFAULT '[]',
    status_json TEXT NOT NULL DEFAULT '{}',
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS printers (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    location TEXT NOT NULL DEFAULT '',
    vendor TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    serial_number TEXT NOT NULL DEFAULT '',
    profile_id TEXT REFERENCES printer_profiles(id) ON DELETE SET NULL,
    is_enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS printer_endpoints (
    id TEXT PRIMARY KEY,
    printer_id TEXT NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('ipp', 'socket', 'agent')),
    uri TEXT NOT NULL,
    dedupe_key TEXT NOT NULL UNIQUE,
    agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    queue_name TEXT NOT NULL DEFAULT '',
    protocol TEXT NOT NULL DEFAULT '',
    mac TEXT NOT NULL DEFAULT '',
    serial_number TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 100,
    is_enabled INTEGER NOT NULL DEFAULT 1,
    health_status TEXT NOT NULL DEFAULT 'unknown',
    last_checked_at TEXT,
    last_success_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    capabilities_json TEXT NOT NULL DEFAULT '{}',
    capabilities_source TEXT NOT NULL DEFAULT 'inferred',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_endpoints_printer ON printer_endpoints(printer_id);

CREATE TABLE IF NOT EXISTS printer_candidates (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    discovered_uri TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'ipp',
    host TEXT NOT NULL DEFAULT '',
    port INTEGER NOT NULL DEFAULT 631,
    path TEXT NOT NULL DEFAULT '/ipp/print',
    dedupe_key TEXT NOT NULL UNIQUE,
    mac TEXT NOT NULL DEFAULT '',
    vendor TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    serial_number TEXT NOT NULL DEFAULT '',
    raw_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'approved', 'ignored', 'merged')),
    possible_ip_change INTEGER NOT NULL DEFAULT 0,
    merged_printer_id TEXT,
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_candidates_status ON printer_candidates(status);

CREATE TABLE IF NOT EXISTS files (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    original_name TEXT NOT NULL,
    mime TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    storage_path TEXT NOT NULL,
    page_count INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_owner ON files(owner_id);
CREATE INDEX IF NOT EXISTS idx_files_expiry ON files(expires_at);

CREATE TABLE IF NOT EXISTS print_jobs (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    printer_id TEXT NOT NULL REFERENCES printers(id),
    endpoint_id TEXT REFERENCES printer_endpoints(id),
    agent_id TEXT REFERENCES agents(id),
    original_file_id TEXT REFERENCES files(id),
    pdf_file_id TEXT REFERENCES files(id),
    status TEXT NOT NULL DEFAULT 'queued',
    copies INTEGER NOT NULL DEFAULT 1,
    duplex TEXT NOT NULL DEFAULT 'one-sided',
    color_mode TEXT NOT NULL DEFAULT 'monochrome',
    paper_size TEXT NOT NULL DEFAULT 'A4',
    page_count INTEGER NOT NULL DEFAULT 1,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    retry_of TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    claimed_at TEXT,
    lease_expires_at TEXT,
    started_at TEXT,
    finished_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_jobs_user ON print_jobs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON print_jobs(status, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_printer ON print_jobs(printer_id, status);

CREATE TABLE IF NOT EXISTS printer_discovery_runs (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    cidr TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'running',
    started_at TEXT NOT NULL,
    finished_at TEXT,
    found_count INTEGER NOT NULL DEFAULT 0,
    error_message TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS printer_endpoint_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    endpoint_id TEXT NOT NULL REFERENCES printer_endpoints(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    detail_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_endpoint_events ON printer_endpoint_events(endpoint_id, id DESC);

CREATE TABLE IF NOT EXISTS audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
