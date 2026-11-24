package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cloudprint/internal/database"
	"cloudprint/internal/store"
)

// newTestStore opens a store on a migrated throwaway database, so migrations
// (including the dispatched_at column) are exercised exactly as at startup.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.New(db)
}

func newTestJob(t *testing.T, st *store.Store) *store.Job {
	t.Helper()
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "dispatcher", "Dispatcher", "x", "user", 0)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	p, err := st.CreatePrinter(ctx, store.NewPrinter{DisplayName: "Test Printer"})
	if err != nil {
		t.Fatalf("create printer: %v", err)
	}
	if _, err := st.CreateEndpoint(ctx, p.ID, store.NewEndpoint{Kind: "ipp", URI: "ipp://127.0.0.1/ipp/print"}); err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	f, err := st.CreateFile(ctx, u.ID, "pdf", "doc.pdf", "application/pdf", "sha", "/tmp/doc.pdf", 1024, 4, 0)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	job, err := st.CreateJob(ctx, store.NewJob{
		UserID: u.ID, PrinterID: p.ID, FileID: f.ID, Copies: 1,
		Duplex: "one-sided", PaperSize: "A4", Pages: 4,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

// MarkDispatched must persist, and only once: a later call (e.g. a retry of the
// same row) must not move the handoff time the device timers count from.
func TestMarkDispatchedPersistsAndIsWriteOnce(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	job := newTestJob(t, st)

	claimAndPrint(t, st, job.ID)
	if got, _ := st.GetJob(ctx, job.ID); got.DispatchedAt != "" {
		t.Fatalf("dispatched_at = %q before handoff, want empty", got.DispatchedAt)
	}

	if err := st.MarkDispatched(ctx, job.ID); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	first, _ := st.GetJob(ctx, job.ID)
	if first.DispatchedAt == "" {
		t.Fatal("dispatched_at was not recorded")
	}
	if err := st.MarkDispatched(ctx, job.ID); err != nil {
		t.Fatalf("second mark dispatched: %v", err)
	}
	second, _ := st.GetJob(ctx, job.ID)
	if second.DispatchedAt != first.DispatchedAt {
		t.Errorf("dispatched_at moved from %q to %q", first.DispatchedAt, second.DispatchedAt)
	}
}

// A job that has been handed to the device must never be failed by the
// dead-submit sweep, however long it has been printing; the sweep only catches
// processes that died mid-render.
func TestRequeueExpiredClaimsSparesDispatchedJobs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	job := newTestJob(t, st)
	claimAndPrint(t, st, job.ID)

	// Age the row well past the sweep threshold, as a long render would.
	past := time.Now().UTC().Add(-26 * time.Minute).Format(time.RFC3339)
	if _, err := st.DB.ExecContext(ctx,
		`UPDATE print_jobs SET started_at = ?, dispatched_at = ? WHERE id = ?`, past, past, job.ID); err != nil {
		t.Fatalf("age job: %v", err)
	}
	if _, err := st.RequeueExpiredClaims(ctx); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if got, _ := st.GetJob(ctx, job.ID); got.Status != "printing" {
		t.Fatalf("dispatched job status = %q, want it left alone", got.Status)
	}

	// The same row without a handoff stamp is a dead process and must be failed.
	if _, err := st.DB.ExecContext(ctx, `UPDATE print_jobs SET dispatched_at = '' WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("clear dispatched: %v", err)
	}
	if _, err := st.RequeueExpiredClaims(ctx); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if got, _ := st.GetJob(ctx, job.ID); got.Status != "failed" {
		t.Fatalf("un-dispatched job status = %q, want failed", got.Status)
	}
}

func mustEndpointID(t *testing.T, st *store.Store, jobID string) string {
	t.Helper()
	job, err := st.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	p, err := st.GetPrinter(context.Background(), job.PrinterID)
	if err != nil || len(p.Endpoints) == 0 {
		t.Fatalf("get printer endpoints: %v", err)
	}
	return p.Endpoints[0].ID
}

// claimAndPrint walks a freshly created job through the same states Dispatch
// uses, with the production claim lease, so the test fails if a long budget
// ever makes the lease expire immediately.
func claimAndPrint(t *testing.T, st *store.Store, jobID string) {
	t.Helper()
	ctx := context.Background()
	epID := mustEndpointID(t, st, jobID)
	claimed, err := st.ClaimQueuedJob(ctx, jobID, epID, 20*time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim job: %v (claimed=%v)", err, claimed)
	}
	if err := st.MarkPrinting(ctx, jobID, epID); err != nil {
		t.Fatalf("mark printing: %v", err)
	}
}
