-- +no-transaction
-- The Windows relay agent is gone. Printing now goes through the local CUPS
-- spooler, which owns the vendor driver (PPD) and the device-specific filter.
-- A queue endpoint's uri is the CUPS queue name, e.g. "pantum-ms6000nw".

UPDATE printer_endpoints SET is_enabled = 0, health_status = 'disabled',
    last_error = 'Windows 转发已停用，请改为在 CloudPrint 服务器上用 CUPS 重新接入',
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE kind = 'agent';

UPDATE printer_endpoints SET is_enabled = 0, health_status = 'disabled',
    last_error = '该地址无法直接出纸，请用 CUPS 队列或 PWG 直连重新接入',
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE kind = 'socket';

CREATE TABLE IF NOT EXISTS printer_endpoints_v2 (
    id TEXT PRIMARY KEY,
    printer_id TEXT NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('ipp', 'cups')),
    uri TEXT NOT NULL,
    dedupe_key TEXT NOT NULL UNIQUE,
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

INSERT INTO printer_endpoints_v2 (id, printer_id, kind, uri, dedupe_key,
    queue_name, protocol, mac, serial_number, priority, is_enabled, health_status,
    last_checked_at, last_success_at, last_error, capabilities_json,
    capabilities_source, created_at, updated_at)
SELECT id, printer_id, kind, uri, dedupe_key,
    queue_name, protocol, mac, serial_number, priority, is_enabled, health_status,
    last_checked_at, last_success_at, last_error, capabilities_json,
    capabilities_source, created_at, updated_at
FROM printer_endpoints
WHERE kind IN ('ipp', 'cups');

DROP TABLE printer_endpoints;
ALTER TABLE printer_endpoints_v2 RENAME TO printer_endpoints;
CREATE INDEX IF NOT EXISTS idx_endpoints_printer ON printer_endpoints(printer_id);

ALTER TABLE print_jobs ADD COLUMN cups_job_id TEXT NOT NULL DEFAULT '';
