package webapi

import (
	"testing"
	"time"

	"cloudprint/internal/store"
)

// A job that is still rendering has handed nothing to the device, so no
// device-side timer may fire no matter how old started_at is. This is the case
// that failed long documents: the render alone could outlast the 10-minute
// "device lost the job" window.
func TestDeviceOldSinceIgnoresRenderTime(t *testing.T) {
	job := &store.Job{
		StartedAt:    time.Now().UTC().Add(-40 * time.Minute).Format(time.RFC3339),
		DispatchedAt: "",
	}
	if deviceOldSince(job, 10*time.Minute) {
		t.Error("a job with no dispatched stamp must not trip a device timer")
	}
}

// Once handed off, the timer counts from the handoff, not from the claim.
func TestDeviceOldSinceCountsFromDispatch(t *testing.T) {
	job := &store.Job{
		StartedAt:    time.Now().UTC().Add(-40 * time.Minute).Format(time.RFC3339),
		DispatchedAt: time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
	}
	if deviceOldSince(job, 10*time.Minute) {
		t.Error("2 minutes since handoff must not trip a 10-minute timer")
	}
	job.DispatchedAt = time.Now().UTC().Add(-11 * time.Minute).Format(time.RFC3339)
	if !deviceOldSince(job, 10*time.Minute) {
		t.Error("11 minutes since handoff must trip a 10-minute timer")
	}
}
