package printer

import (
	"testing"
	"time"

	"cloudprint/internal/store"
)

// Rendering is what makes a job slow, so the budget has to grow with the page
// count. A fixed three minutes failed every document longer than roughly sixty
// pages before the printer was ever contacted.
func TestDispatchBudgetGrowsWithPageCount(t *testing.T) {
	small := DispatchBudget(1)
	large := DispatchBudget(150)

	if small < 2*time.Minute {
		t.Errorf("budget for one page = %s, want at least 2m so a cold start fits", small)
	}
	if large <= small {
		t.Errorf("budget for 150 pages = %s, not larger than %s for one page", large, small)
	}
	if large < 5*time.Minute {
		t.Errorf("budget for 150 pages = %s, too small to render them", large)
	}
}

// The cap keeps a stuck job from holding a printer's only slot indefinitely.
func TestDispatchBudgetIsCapped(t *testing.T) {
	if got := DispatchBudget(5000); got != 20*time.Minute {
		t.Errorf("budget = %s, want the 20m cap", got)
	}
	if got := DispatchBudget(0); got != DispatchBudget(1) {
		t.Errorf("budget for 0 pages = %s, want the one-page budget", got)
	}
	if got := DispatchBudget(-5); got != DispatchBudget(1) {
		t.Errorf("budget for negative pages = %s, want the one-page budget", got)
	}
}

// The claim is the printer's only slot guard and is taken before rendering, so
// its lease has to cover the whole dispatch budget; a 5-minute lease let the
// requeue sweep free the slot while a long job was still rasterizing.
func TestClaimLeaseCoversDispatchBudget(t *testing.T) {
	for _, pages := range []int{0, 1, 60, 150, 5000} {
		if got, want := ClaimLease(pages), DispatchBudget(pages); got != want {
			t.Errorf("ClaimLease(%d) = %s, want %s", pages, got, want)
		}
	}
	if got := ClaimLease(5000); got < 20*time.Minute {
		t.Errorf("ClaimLease(5000) = %s, want at least the 20m budget cap", got)
	}
}

// JobPages picks the same page count the budget is sized against, and falls
// back to a single page for a job that has not been measured yet.
func TestJobPages(t *testing.T) {
	if got := JobPages(nil); got != 1 {
		t.Errorf("JobPages(nil) = %d, want 1", got)
	}
	// GetJob stores page_count per copy and derives total_pages from copies;
	// stored jobs carry both, so the total wins.
	if got := JobPages(&store.Job{PageCount: 10, TotalPages: 30, Copies: 3}); got != 30 {
		t.Errorf("JobPages(total) = %d, want 30", got)
	}
	if got := JobPages(&store.Job{PageCount: 7}); got != 7 {
		t.Errorf("JobPages(page_count) = %d, want 7", got)
	}
	if got := JobPages(&store.Job{}); got != 1 {
		t.Errorf("JobPages(empty) = %d, want 1", got)
	}
}
