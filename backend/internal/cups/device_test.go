package cups

import (
	"encoding/binary"
	"testing"

	"cloudprint/internal/ipp"
)

// A device's Get-Jobs response carries one job-attributes group per job. This
// builds the smallest valid frame so the matching rules are tested without a
// printer on the network.
func buildJobsResponse(jobs []struct {
	id    int32
	name  string
	state int32
}) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint16(buf[0:2], ipp.Version11)
	// status: successful-ok
	binary.BigEndian.PutUint16(buf[2:4], 0)
	binary.BigEndian.PutUint32(buf[4:8], 1)
	writeAttr := func(tag byte, name string, value []byte) {
		buf = append(buf, tag)
		buf = append(buf, byte(len(name)>>8), byte(len(name)))
		buf = append(buf, name...)
		// Every IPP value, including integers, carries a 2-byte length prefix.
		buf = append(buf, byte(len(value)>>8), byte(len(value)))
		buf = append(buf, value...)
	}
	for _, j := range jobs {
		buf = append(buf, ipp.TagJobAttributes)
		iv := make([]byte, 4)
		binary.BigEndian.PutUint32(iv, uint32(j.id))
		writeAttr(ipp.ValueInteger, "job-id", iv)
		sv := make([]byte, 4)
		binary.BigEndian.PutUint32(sv, uint32(j.state))
		writeAttr(ipp.ValueEnum, "job-state", sv)
		writeAttr(ipp.ValueNameWithoutLang, "job-name", []byte(j.name))
	}
	buf = append(buf, ipp.TagEndOfAttributes)
	return buf
}

func TestDeviceVerdictUsesNewestMatchingJob(t *testing.T) {
	raw := buildJobsResponse([]struct {
		id    int32
		name  string
		state int32
	}{
		{id: 103, name: "cloudprint-test.pdf", state: 9},
		{id: 106, name: "cloudprint-test.pdf", state: 8},
	})
	msg, err := ipp.ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ok, known := deviceJobVerdict(msg, "cloudprint-test.pdf")
	if !known || ok != "failed" {
		t.Fatalf("aborted newest job must be failed (verdict=%q known=%v)", ok, known)
	}
}

func TestDeviceVerdictIgnoresOtherJobNames(t *testing.T) {
	raw := buildJobsResponse([]struct {
		id    int32
		name  string
		state int32
	}{
		{id: 107, name: "other.pdf", state: 8},
	})
	msg, err := ipp.ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, known := deviceJobVerdict(msg, "cloudprint-test.pdf"); known {
		t.Fatal("a different job name must not answer for this job")
	}
}

func TestDeviceVerdictPendingIsUnknown(t *testing.T) {
	raw := buildJobsResponse([]struct {
		id    int32
		name  string
		state int32
	}{
		{id: 108, name: "another-file.pdf", state: 5},
	})
	msg, err := ipp.ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, known := deviceJobVerdict(msg, "cloudprint-test.pdf"); known {
		t.Fatal("a different job name must not answer for this job")
	}
}

func TestDeviceVerdictProcessingIsPending(t *testing.T) {
	raw := buildJobsResponse([]struct {
		id    int32
		name  string
		state int32
	}{
		{id: 109, name: "cloudprint-test.pdf", state: 5},
	})
	msg, err := ipp.ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	verdict, known := deviceJobVerdict(msg, "cloudprint-test.pdf")
	if !known || verdict != "pending" {
		t.Fatalf("a job the device still owns must stay pending (got %q)", verdict)
	}
}
