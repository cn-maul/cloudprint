package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTP       *http.Client
	EndpointID string
	Version    string
}

func New(baseURL, token, endpointID, version string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		EndpointID: endpointID,
		Version:    version,
		HTTP:       &http.Client{Timeout: 90 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("server %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type HeartbeatRequest struct {
	Version string `json:"version"`
	Queues  any    `json:"queues"`
	Status  any    `json:"status"`
}

func (c *Client) Heartbeat(ctx context.Context, queues, status any) error {
	return c.do(ctx, http.MethodPost, "/api/agent/heartbeat", HeartbeatRequest{
		Version: c.Version, Queues: queues, Status: status,
	}, nil)
}

type Job struct {
	ID          string `json:"id"`
	PrinterID   string `json:"printer_id"`
	PrinterName string `json:"printer_name"`
	FileName    string `json:"file_name"`
	PDFFile     string `json:"pdf_file_id"`
	Status      string `json:"status"`
	Copies      int    `json:"copies"`
	Duplex      string `json:"duplex"`
	PaperSize   string `json:"paper_size"`
	PageCount   int    `json:"page_count"`
}

type ClaimResult struct {
	Job          *Job   `json:"job"`
	DownloadURL  string `json:"download_url"`
	LeaseSeconds int    `json:"lease_seconds"`
	PrintCommand string `json:"print_command"`
}

func (c *Client) Claim(ctx context.Context, leaseSeconds int) (*ClaimResult, error) {
	var out ClaimResult
	err := c.do(ctx, http.MethodPost, "/api/agent/claim", map[string]any{
		"endpoint_id": c.EndpointID, "lease_seconds": leaseSeconds,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Job == nil {
		return nil, nil
	}
	return &out, nil
}

func (c *Client) Start(ctx context.Context, jobID string) error {
	return c.do(ctx, http.MethodPost, "/api/agent/jobs/"+jobID+"/start", nil, nil)
}

type Result struct {
	Success      bool   `json:"success"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

func (c *Client) Complete(ctx context.Context, jobID string, res Result) error {
	return c.do(ctx, http.MethodPost, "/api/agent/jobs/"+jobID+"/result", res, nil)
}

func (c *Client) Download(ctx context.Context, fileID, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/agent/files/"+fileID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("download %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	f, err := osCreate(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

var ErrNoJob = errors.New("no job")
