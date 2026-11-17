ALTER TABLE printers ADD COLUMN network_address TEXT NOT NULL DEFAULT '';

-- Candidates left behind by older discovery runs: LPD/IPPS ports and devices
-- that opened 631 but never spoke IPP. They are not addable printers and only
-- confuse the admin.
UPDATE printer_candidates
SET status = 'ignored'
WHERE status = 'new'
  AND (
      (kind = 'ipp' AND port <> 631)
      OR (kind = 'socket' AND port <> 9100)
      OR raw_json LIKE '%"ipp_unanswered":true%'
  );
