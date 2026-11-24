package ipp

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// printJobResponse builds a successful Print-Job reply that names the new job.
func printJobResponse(id int32, uri string) []byte {
	body := make([]byte, 8)
	binary.BigEndian.PutUint16(body[0:2], Version11)
	binary.BigEndian.PutUint16(body[2:4], 0)
	binary.BigEndian.PutUint32(body[4:8], 1)
	body = append(body, TagJobAttributes)
	body = appendAttribute(body, "job-id", ValueInteger, EncodeInt(id))
	body = appendAttribute(body, "job-uri", ValueURI, []byte(uri))
	body = append(body, TagEndOfAttributes)
	return body
}

// A document handed over as a reader must reach the device byte for byte, with
// an explicit Content-Length: printers reject chunked bodies, and the job's own
// length is the only way they can tell a truncated upload from a complete one.
func TestPrintJobStreamsDocumentFromReader(t *testing.T) {
	payload := make([]byte, 2<<20)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	var declared int64 = -2
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		declared = r.ContentLength
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		received = body
		w.Header().Set("Content-Type", "application/ipp")
		_, _ = w.Write(printJobResponse(42, "ipp://device/ipp/print/job-id:0000002a"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, 10*time.Second)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	id, uri, err := client.PrintJob(context.Background(), PrintOptions{
		JobName:      "stream",
		Format:       "image/pwg-raster",
		Copies:       1,
		Document:     bytes.NewReader(payload),
		DocumentSize: int64(len(payload)),
	})
	if err != nil {
		t.Fatalf("print job: %v", err)
	}
	if id != 42 {
		t.Errorf("job id = %d, want 42", id)
	}
	if uri != "ipp://device/ipp/print/job-id:0000002a" {
		t.Errorf("job uri = %q", uri)
	}

	// The declared length must describe the whole request, not just the
	// document: operation attributes are written before the stream starts.
	if declared != int64(len(received)) {
		t.Errorf("declared Content-Length = %d, received %d bytes", declared, len(received))
	}
	if len(received) <= len(payload) {
		t.Fatalf("request is %d bytes, expected the attributes plus %d bytes of document",
			len(received), len(payload))
	}
	if got := received[len(received)-len(payload):]; !bytes.Equal(got, payload) {
		t.Error("document was not written verbatim")
	}
}

// An empty document still needs a valid request; a nil reader must not turn
// into a body the device cannot parse.
func TestPrintJobWithoutDocumentSendsAttributesOnly(t *testing.T) {
	var declared int64 = -2
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		declared = r.ContentLength
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/ipp")
		_, _ = w.Write(printJobResponse(7, ""))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, 10*time.Second)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, _, err := client.PrintJob(context.Background(), PrintOptions{JobName: "empty"}); err != nil {
		t.Fatalf("print job: %v", err)
	}
	if declared != int64(len(received)) || declared <= 0 {
		t.Errorf("declared Content-Length = %d, received %d bytes", declared, len(received))
	}
}
