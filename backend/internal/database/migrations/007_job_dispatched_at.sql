-- Rendering happens between the claim and the moment the document actually
-- reaches the device, and how long it takes depends on the page count. The
-- device-side timeouts (lost job, stalled job, unreachable printer) used to
-- count from started_at, so a long render ate most of their window and the job
-- was failed while it was still being rasterized. dispatched_at records the
-- handoff so those timers measure device time only.
ALTER TABLE print_jobs ADD COLUMN dispatched_at TEXT NOT NULL DEFAULT '';

-- Jobs created before this column existed have already been handed off if they
-- ever got a device job id.
UPDATE print_jobs
SET dispatched_at = started_at
WHERE dispatched_at = '' AND started_at IS NOT NULL AND COALESCE(cups_job_id, '') != '';
