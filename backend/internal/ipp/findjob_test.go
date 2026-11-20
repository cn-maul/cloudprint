package ipp

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// jobsResponse builds a Get-Jobs reply with one job-attributes group per job.
type testJob struct {
	id    int32
	name  string
	uri   string
	state int32
}

func jobsResponse(jobs []testJob) []byte {
	body := make([]byte, 8)
	binary.BigEndian.PutUint16(body[0:2], Version11)
	binary.BigEndian.PutUint16(body[2:4], 0)
	binary.BigEndian.PutUint32(body[4:8], 1)
	for _, j := range jobs {
		body = append(body, TagJobAttributes)
		body = appendAttribute(body, "job-id", ValueInteger, EncodeInt(j.id))
		body = appendAttribute(body, "job-name", ValueNameWithoutLang, []byte(j.name))
		body = appendAttribute(body, "job-uri", ValueURI, []byte(j.uri))
		body = appendAttribute(body, "job-state", ValueEnum, EncodeInt(j.state))
	}
	body = append(body, TagEndOfAttributes)
	return body
}

func appendAttribute(dst []byte, name string, tag byte, value []byte) []byte {
	dst = append(dst, tag)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(name)))
	dst = append(dst, name...)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(value)))
	return append(dst, value...)
}

func TestFindJobURIResolvesMissingURIByJobName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/ipp")
		_, _ = w.Write(jobsResponse([]testJob{
			{id: 116, name: "j_other", uri: "ipp://dev/ipp/print/job-id:00000074", state: 3},
			{id: 117, name: "j_target", uri: "ipp://dev/ipp/print/job-id:00000075", state: 3},
		}))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL+"/ipp/print", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	uri, id, ok := c.FindJobURI(context.Background(), "j_target")
	if !ok {
		t.Fatal("job was not found by name")
	}
	if uri != "ipp://dev/ipp/print/job-id:00000075" || id != 117 {
		t.Fatalf("got uri=%q id=%d", uri, id)
	}
}

func TestFindJobURIPicksNewestDuplicateName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/ipp")
		_, _ = w.Write(jobsResponse([]testJob{
			{id: 200, name: "dup", uri: "ipp://dev/ipp/print/job-id:000000c8", state: 9},
			{id: 201, name: "dup", uri: "ipp://dev/ipp/print/job-id:000000c9", state: 3},
		}))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL+"/ipp/print", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	uri, id, ok := c.FindJobURI(context.Background(), "dup")
	if !ok || id != 201 || !strings.HasSuffix(uri, "000000c9") {
		t.Fatalf("got uri=%q id=%d ok=%v", uri, id, ok)
	}
}

func TestFindJobURIMissingNameReportsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/ipp")
		_, _ = w.Write(jobsResponse([]testJob{
			{id: 1, name: "something-else", uri: "ipp://dev/ipp/print/job-id:00000001", state: 9},
		}))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL+"/ipp/print", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.FindJobURI(context.Background(), "absent"); ok {
		t.Fatal("an unknown job name must not resolve")
	}
}
