-- +no-transaction
-- Deleting a printer used to fail: print_jobs.printer_id had no ON DELETE
-- clause, so the foreign key blocked the delete. Job history must survive the
-- printer's removal, so the job now keeps a display-name snapshot and the
-- foreign key is set to NULL on delete.

ALTER TABLE print_jobs ADD COLUMN printer_name TEXT NOT NULL DEFAULT '';

UPDATE print_jobs
SET printer_name = COALESCE((SELECT p.display_name FROM printers p WHERE p.id = print_jobs.printer_id), '')
WHERE printer_name = '';

CREATE TABLE IF NOT EXISTS print_jobs_v2 (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    printer_id TEXT REFERENCES printers(id) ON DELETE SET NULL,
    printer_name TEXT NOT NULL DEFAULT '',
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
    finished_at TEXT,
    cups_job_id TEXT NOT NULL DEFAULT ''
);

INSERT INTO print_jobs_v2 (id, user_id, printer_id, printer_name, endpoint_id,
    agent_id, original_file_id, pdf_file_id, status, copies, duplex, color_mode,
    paper_size, page_count, error_code, error_message, retry_of, created_at,
    updated_at, claimed_at, lease_expires_at, started_at, finished_at, cups_job_id)
SELECT id, user_id, printer_id, printer_name, endpoint_id,
    agent_id, original_file_id, pdf_file_id, status, copies, duplex, color_mode,
    paper_size, page_count, error_code, error_message, retry_of, created_at,
    updated_at, claimed_at, lease_expires_at, started_at, finished_at, cups_job_id
FROM print_jobs;

DROP TABLE print_jobs;
ALTER TABLE print_jobs_v2 RENAME TO print_jobs;

CREATE INDEX IF NOT EXISTS idx_jobs_user ON print_jobs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON print_jobs(status, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_printer ON print_jobs(printer_id, status);
