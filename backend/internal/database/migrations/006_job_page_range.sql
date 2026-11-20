-- The job may print a subset of the uploaded document. page_count then holds
-- the number of selected pages (per copy), and page_range keeps the text the
-- user typed so the queue can show exactly what was requested.
ALTER TABLE print_jobs ADD COLUMN page_range TEXT NOT NULL DEFAULT '';
