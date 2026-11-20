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

type NewJob struct {
	UserID    string `json:"-"`
	PrinterID string `json:"printer_id"`
	FileID    string `json:"file_id"`
	Copies    int    `json:"copies"`
	Duplex    string `json:"duplex"`
	ColorMode string `json:"color_mode"`
	PaperSize string `json:"paper_size"`
	PageRange string `json:"page_range"`
	RetryOf   string `json:"-"`
	PDFFileID string `json:"-"`
	Pages     int    `json:"-"`
	// OriginalFileID keeps a retry pointing at the upload the user chose, not
	// at the temporary page-range PDF derived from it.
	OriginalFileID string `json:"-"`
}

func (s *Store) PagesToday(ctx context.Context, userID string) (int, error) {
	start := startOfToday()
	var n sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT SUM(page_count * copies) FROM print_jobs
		WHERE user_id = ? AND created_at >= ? AND status NOT IN ('rejected','canceled')`,
		userID, start).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n.Int64), nil
}

// startOfToday uses the NAS's local clock so "daily quota" resets at local
// midnight rather than 08:00 China time.
func startOfToday() string {
	now := time.Now()
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).UTC().Format(time.RFC3339)
}

func (s *Store) CreateJob(ctx context.Context, in NewJob) (*Job, error) {
	printer, err := s.GetPrinter(ctx, in.PrinterID)
	if err != nil {
		return nil, err
	}
	if !printer.IsEnabled {
		return nil, fmt.Errorf("该打印机已停用")
	}
	file, err := s.GetFile(ctx, in.FileID)
	if err != nil {
		return nil, fmt.Errorf("文件不存在或已过期")
	}
	if file.OwnerID != in.UserID {
		return nil, fmt.Errorf("无权使用该文件")
	}
	if file.Kind != "pdf" {
		return nil, fmt.Errorf("文件尚未完成转换")
	}
	if file.StoragePath == "" {
		return nil, fmt.Errorf("文件已超过保留期并被清理，请重新上传")
	}
	in.PDFFileID = file.ID
	if in.Copies < 1 {
		in.Copies = 1
	}
	if in.Copies > 99 {
		in.Copies = 99
	}
	if in.Duplex == "" {
		in.Duplex = "one-sided"
	}
	if in.Duplex != "one-sided" && in.Duplex != "two-sided-long-edge" && in.Duplex != "two-sided-short-edge" {
		return nil, fmt.Errorf("无效的双面参数")
	}
	if in.PaperSize == "" {
		in.PaperSize = "A4"
	}
	if in.Pages < 0 || in.Pages > file.PageCount {
		return nil, fmt.Errorf("页码超出文件范围")
	}
	if in.Pages == 0 {
		in.Pages = file.PageCount
	}
	if in.ColorMode == "" {
		in.ColorMode = "monochrome"
	}
	if in.ColorMode != "monochrome" && in.ColorMode != "color" {
		return nil, fmt.Errorf("无效的颜色参数")
	}
	now := Now()
	originalFile := in.OriginalFileID
	if originalFile == "" {
		originalFile = file.ID
	}
	j := &Job{
		ID:           "j_" + uuid.NewString(),
		UserID:       in.UserID,
		PrinterID:    printer.ID,
		PrinterName:  printer.DisplayName,
		OriginalFile: originalFile,
		PDFFile:      in.PDFFileID,
		FileName:     file.OriginalName,
		Status:       "queued",
		Copies:       in.Copies,
		Duplex:       in.Duplex,
		ColorMode:    in.ColorMode,
		PaperSize:    in.PaperSize,
		PageCount:    in.Pages,
		TotalPages:   in.Pages * in.Copies,
		PageRange:    in.PageRange,
		RetryOf:      in.RetryOf,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	var retry any
	if j.RetryOf != "" {
		retry = j.RetryOf
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO print_jobs
		(id, user_id, printer_id, printer_name, original_file_id, pdf_file_id,
		status, copies, duplex, color_mode, paper_size, page_count, page_range, retry_of,
		created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.UserID, j.PrinterID, j.PrinterName, j.OriginalFile, j.PDFFile,
		j.Copies, j.Duplex, j.ColorMode, j.PaperSize, j.PageCount, j.PageRange, retry,
		j.CreatedAt, j.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return j, nil
}

const jobSelect = `SELECT j.id, j.user_id, COALESCE(u.display_name, u.username, '') AS username,
	COALESCE(j.printer_id, ''),
	COALESCE(NULLIF(p.display_name, ''), j.printer_name, '') AS printer_name,
	COALESCE(j.endpoint_id, ''),
	COALESCE(e.uri, ''), COALESCE(j.original_file_id, ''),
	COALESCE(j.pdf_file_id, ''), COALESCE(f.original_name, ''), j.status, j.copies, j.duplex,
	j.color_mode, j.paper_size, j.page_count, j.error_code, j.error_message,
	COALESCE(j.retry_of, ''), j.created_at, j.updated_at, COALESCE(j.claimed_at, ''),
	COALESCE(j.lease_expires_at, ''), COALESCE(j.started_at, ''), COALESCE(j.finished_at, ''),
	COALESCE(e.queue_name, ''), COALESCE(j.cups_job_id, ''), COALESCE(j.page_range, '')
	FROM print_jobs j
	LEFT JOIN users u ON u.id = j.user_id
	LEFT JOIN printers p ON p.id = j.printer_id
	LEFT JOIN printer_endpoints e ON e.id = j.endpoint_id
	LEFT JOIN files f ON f.id = j.pdf_file_id`

func scanJob(row rowScanner) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.UserID, &j.Username, &j.PrinterID, &j.PrinterName,
		&j.EndpointID, &j.EndpointURI, &j.OriginalFile, &j.PDFFile,
		&j.FileName, &j.Status, &j.Copies, &j.Duplex, &j.ColorMode, &j.PaperSize,
		&j.PageCount, &j.ErrorCode, &j.ErrorMessage, &j.RetryOf, &j.CreatedAt,
		&j.UpdatedAt, &j.ClaimedAt, &j.LeaseExpires, &j.StartedAt, &j.FinishedAt,
		&j.QueueName, &j.CUPSJobID, &j.PageRange)
	j.TotalPages = j.PageCount * j.Copies
	return j, err
}

func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.DB.QueryRowContext(ctx, jobSelect+` WHERE j.id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]Job, int, error) {
	where := []string{"1=1"}
	args := []any{}
	if f.UserID != "" {
		where = append(where, "j.user_id = ?")
		args = append(args, f.UserID)
	}
	if f.PrinterID != "" {
		where = append(where, "j.printer_id = ?")
		args = append(args, f.PrinterID)
	}
	if len(f.Statuses) > 0 {
		ph := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			ph[i] = "?"
			args = append(args, st)
		}
		where = append(where, "j.status IN ("+strings.Join(ph, ",")+")")
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs j WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.DB.QueryContext(ctx, jobSelect+` WHERE `+strings.Join(where, " AND ")+
		` ORDER BY j.created_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

func (s *Store) CancelJob(ctx context.Context, id, userID string, isAdmin bool) error {
	q := `UPDATE print_jobs SET status = 'canceled', updated_at = ?, finished_at = ?
		WHERE id = ? AND status = 'queued'`
	args := []any{Now(), Now(), id}
	if !isAdmin {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("任务不在可取消状态")
	}
	return nil
}

// ClaimJob atomically reserves the next queued job for an endpoint, enforcing
// one active job per logical printer.
func (s *Store) ClaimJob(ctx context.Context, printerID, endpointID string, lease time.Duration) (*Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs
		WHERE printer_id = ? AND status IN ('claimed','printing')`, printerID).Scan(&busy); err != nil {
		return nil, err
	}
	if busy > 0 {
		return nil, nil
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM print_jobs
		WHERE printer_id = ? AND status = 'queued'
		ORDER BY created_at LIMIT 1`, printerID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	leaseExp := now.Add(lease).Format(time.RFC3339)
	res, err := tx.ExecContext(ctx, `UPDATE print_jobs SET status = 'claimed', endpoint_id = ?,
		claimed_at = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND status = 'queued'`,
		endpointID, now.Format(time.RFC3339), leaseExp, now.Format(time.RFC3339), id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetJob(ctx, id)
}

func (s *Store) MarkPrinting(ctx context.Context, id, endpointID string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = 'printing', started_at = ?,
		updated_at = ? WHERE id = ? AND status = 'claimed' AND endpoint_id = ?`,
		Now(), Now(), id, endpointID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("任务状态不允许开始打印")
	}
	return nil
}

func (s *Store) ReleaseClaim(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = 'queued',
		endpoint_id = NULL, claimed_at = NULL, lease_expires_at = NULL,
		updated_at = ? WHERE id = ? AND status = 'claimed'`, Now(), id)
	return err
}

func (s *Store) CompleteJob(ctx context.Context, id, endpointID string, ok bool, code, message string) error {
	status := "completed"
	if !ok {
		status = "failed"
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = ?, error_code = ?,
		error_message = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND endpoint_id = ? AND status IN ('claimed','printing')`,
		status, code, truncate(message, 1000), Now(), Now(), id, endpointID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("任务状态不允许完成")
	}
	return nil
}

func (s *Store) RequeueExpiredClaims(ctx context.Context) (int, error) {
	// A local CUPS submit is synchronous, so a row stuck in "printing" means
	// the process died mid-submit. Its CUPS job id is unknown or unusable;
	// fail it instead of silently reprinting a document the user already got.
	res, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = 'failed',
		error_code = 'SUBMIT_TIMEOUT',
		error_message = '提交打印超时，请确认纸张是否已出；如未出纸请重新提交',
		finished_at = ?, updated_at = ?
		WHERE status = 'printing' AND started_at IS NOT NULL
		AND started_at < ? AND COALESCE(cups_job_id, '') = ''`,
		Now(), Now(), time.Now().UTC().Add(-10*time.Minute).Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	stale, _ := res.RowsAffected()
	res, err = s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = 'queued',
		endpoint_id = NULL, claimed_at = NULL, lease_expires_at = NULL,
		updated_at = ? WHERE status = 'claimed' AND lease_expires_at < ?`,
		Now(), Now())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n) + int(stale), nil
}

// SetJobCUPSID links a print job to the spooler job so its state can be
// polled after submission.
func (s *Store) SetJobCUPSID(ctx context.Context, id, cupsJob string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET cups_job_id = ?,
		updated_at = ? WHERE id = ?`, cupsJob, Now(), id)
	return err
}

// PrintingCUPSJobs lists jobs submitted to a CUPS queue whose paper has not
// been confirmed yet. A job is only "completed" once the device no longer
// holds it, so the UI never claims success for a document that is still in
// the queue or was aborted.
//
// Jobs without a stored device reference are included on purpose: a device
// that omits job-uri from its Print-Job reply leaves exactly that state, and
// excluding them would strand the job in "printing" forever.
func (s *Store) PrintingCUPSJobs(ctx context.Context, limit int) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, jobSelect+` WHERE j.status = 'printing'
		ORDER BY j.created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FailJob marks a job that was already accepted by an endpoint as failed.
func (s *Store) FailJob(ctx context.Context, id, code, message string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = 'failed',
		error_code = ?, error_message = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = 'printing'`,
		code, truncate(message, 1000), Now(), Now(), id)
	return err
}

// SetJobNote attaches a transient explanation to an in-flight job without
// changing its status. Used when the device reports a recoverable problem,
// e.g. media-empty, while the spooler still holds the job.
func (s *Store) SetJobNote(ctx context.Context, id, code, message string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET error_code = ?,
		error_message = ?, updated_at = ? WHERE id = ? AND status = 'printing'`,
		code, truncate(message, 1000), Now(), id)
	return err
}

// ClearJobNote removes a transient explanation once the condition it described
// is gone, so a job that recovered does not keep showing "out of paper".
func (s *Store) ClearJobNote(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET error_code = NULL,
		error_message = '', updated_at = ? WHERE id = ? AND status = 'printing'`,
		Now(), id)
	return err
}

// ClearFinishedJobs removes a printer's finished job history and returns how
// many rows went away.
//
// Only terminal jobs are eligible. 'queued' and 'claimed' are still waiting to
// print, and 'printing' is in flight on the device, so deleting any of those
// would erase work the user still expects. That includes the stuck 'printing'
// rows: those are deliberately left to the stall watchdog, which fails them
// with an explanation rather than silently dropping them.
func (s *Store) ClearFinishedJobs(ctx context.Context, printerID string) (int, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM print_jobs
		WHERE printer_id = ?
		  AND status IN ('completed','failed','canceled')`, printerID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CancelSubmittedJob marks a job canceled after its spooler job was canceled.
func (s *Store) CancelSubmittedJob(ctx context.Context, id, code, message string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = 'canceled',
		error_code = ?, error_message = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('claimed','printing')`,
		code, truncate(message, 1000), Now(), Now(), id)
	return err
}

// CancelPrintingJobs marks a printer's in-flight jobs canceled after the device
// dropped them (a purge). Without this, the job worker keeps polling a job the
// device no longer knows about and the printer's single active slot stays
// occupied, blocking every later upload.
func (s *Store) CancelPrintingJobs(ctx context.Context, printerID, reason string) (int, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = 'canceled',
		error_code = 'PURGED', error_message = ?, finished_at = ?, updated_at = ?
		WHERE printer_id = ? AND status IN ('claimed','printing')`,
		truncate(reason, 1000), Now(), Now(), printerID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) HasActiveJob(ctx context.Context, printerID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs
		WHERE printer_id = ? AND status IN ('claimed','printing')`, printerID).Scan(&n)
	return n > 0, err
}

// PrinterQueueCounts returns how many jobs each printer has waiting and in
// flight. The printer list needs this to say "2 waiting" without fetching every
// queue separately.
func (s *Store) PrinterQueueCounts(ctx context.Context) (map[string]struct{ Queued, Printing int }, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT printer_id, status, COUNT(*)
		FROM print_jobs WHERE status IN ('queued','claimed','printing')
		GROUP BY printer_id, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{ Queued, Printing int }{}
	for rows.Next() {
		var id string
		var status string
		var n int
		if err := rows.Scan(&id, &status, &n); err != nil {
			return nil, err
		}
		c := out[id]
		if status == "queued" {
			c.Queued = n
		} else {
			c.Printing = n
		}
		out[id] = c
	}
	return out, rows.Err()
}

// ClaimQueuedJob atomically reserves one specific queued job for an endpoint.
// The NOT EXISTS clause makes the one-active-job-per-printer rule part of the
// same statement, so two dispatchers cannot start two jobs on one printer.
func (s *Store) ClaimQueuedJob(ctx context.Context, jobID, endpointID string, lease time.Duration) (*Job, error) {
	now := time.Now().UTC()
	leaseExp := now.Add(lease).Format(time.RFC3339)
	res, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET status = 'claimed',
		endpoint_id = ?, claimed_at = ?, lease_expires_at = ?,
		updated_at = ?
		WHERE id = ? AND status = 'queued'
		AND NOT EXISTS (
			SELECT 1 FROM print_jobs active
			WHERE active.printer_id = print_jobs.printer_id
			AND active.status IN ('claimed','printing')
		)`,
		endpointID, now.Format(time.RFC3339), leaseExp,
		now.Format(time.RFC3339), jobID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, nil
	}
	return s.GetJob(ctx, jobID)
}

func (s *Store) QueuedJobs(ctx context.Context, limit int) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, jobSelect+` WHERE j.status = 'queued'
		ORDER BY j.created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) SetJobPDF(ctx context.Context, id, pdfID string, pages int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE print_jobs SET pdf_file_id = ?, page_count = ?,
		updated_at = ? WHERE id = ?`, pdfID, pages, Now(), id)
	return err
}

func (s *Store) Dashboard(ctx context.Context, userID string) (Dashboard, error) {
	d := Dashboard{StatusCounts: map[string]int{}, RecentJobs: []Job{}}
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM printers`).Scan(&d.PrintersTotal)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM printers WHERE is_enabled = 1`).Scan(&d.PrintersEnabled)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs WHERE status = 'queued'`).Scan(&d.QueuedJobs)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs WHERE status IN ('claimed','printing')`).Scan(&d.PrintingJobs)
	start := startOfToday()
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs WHERE status = 'failed' AND created_at >= ?`, start).Scan(&d.FailedJobsToday)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs WHERE status = 'completed' AND created_at >= ?`, start).Scan(&d.CompletedToday)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM printer_candidates WHERE status = 'new'`).Scan(&d.NewCandidates)

	rows, err := s.DB.QueryContext(ctx, `SELECT status, COUNT(*) FROM print_jobs GROUP BY status`)
	if err == nil {
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				d.StatusCounts[st] = n
			}
		}
		rows.Close()
	}
	// Report configured CUPS queues and how many are healthy.
	var cupsTotal, cupsReady int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM printer_endpoints WHERE kind = 'cups' AND is_enabled = 1`).Scan(&cupsTotal)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM printer_endpoints
		WHERE kind = 'cups' AND is_enabled = 1 AND health_status = 'healthy'`).Scan(&cupsReady)
	d.CUPSReady = cupsReady
	d.CUPSTotal = cupsTotal
	user, err := s.GetUser(ctx, userID)
	if err == nil {
		d.DailyPageQuota = user.DailyPageQuota
		d.DailyPages, _ = s.PagesToday(ctx, userID)
	}
	jobs, _, err := s.ListJobs(ctx, JobFilter{UserID: userID, Limit: 8})
	if err == nil {
		d.RecentJobs = jobs
	}
	return d, nil
}
