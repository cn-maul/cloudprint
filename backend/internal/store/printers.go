package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func (s *Store) ListProfiles(ctx context.Context) ([]Profile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, vendor, model_pattern, transports_json,
		capabilities_json, notes, is_builtin FROM printer_profiles ORDER BY is_builtin DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		var p Profile
		var transports, caps, notes string
		if err := rows.Scan(&p.ID, &p.Vendor, &p.ModelPattern, &transports, &caps, &notes, &p.IsBuiltin); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(transports), &p.Transports)
		_ = json.Unmarshal([]byte(caps), &p.Capabilities)
		if strings.TrimSpace(notes) != "" {
			_ = json.Unmarshal([]byte(notes), &p.Notes)
			if len(p.Notes) == 0 {
				p.Notes = []string{notes}
			}
		}
		if p.Notes == nil {
			p.Notes = []string{}
		}
		if p.Transports == nil {
			p.Transports = []string{}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListPrinters(ctx context.Context, includeDisabled bool) ([]Printer, error) {
	q := `SELECT id, display_name, location, vendor, model, serial_number,
		COALESCE(profile_id, ''), network_address, is_enabled, created_at, updated_at FROM printers`
	if !includeDisabled {
		q += ` WHERE is_enabled = 1`
	}
	q += ` ORDER BY display_name`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Printer{}
	index := map[string]int{}
	for rows.Next() {
		var p Printer
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.Location, &p.Vendor, &p.Model,
			&p.SerialNumber, &p.ProfileID, &p.NetworkAddress, &p.IsEnabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Endpoints = []Endpoint{}
		out = append(out, p)
		index[p.ID] = len(out) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	eps, err := s.ListAllEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	for _, ep := range eps {
		if i, ok := index[ep.PrinterID]; ok {
			out[i].Endpoints = append(out[i].Endpoints, ep)
		}
	}
	for i := range out {
		gids, err := s.PrinterGroupIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].GroupIDs = gids
	}
	return out, nil
}

func (s *Store) GetPrinter(ctx context.Context, id string) (*Printer, error) {
	var p Printer
	row := s.DB.QueryRowContext(ctx, `SELECT id, display_name, location, vendor, model, serial_number,
		COALESCE(profile_id, ''), network_address, is_enabled, created_at, updated_at FROM printers WHERE id = ?`, id)
	err := row.Scan(&p.ID, &p.DisplayName, &p.Location, &p.Vendor, &p.Model,
		&p.SerialNumber, &p.ProfileID, &p.NetworkAddress, &p.IsEnabled, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	eps, err := s.ListEndpoints(ctx, id)
	if err != nil {
		return nil, err
	}
	p.Endpoints = eps
	p.GroupIDs, _ = s.PrinterGroupIDs(ctx, p.ID)
	return &p, nil
}

type NewPrinter struct {
	DisplayName    string `json:"display_name"`
	Location       string `json:"location"`
	Vendor         string `json:"vendor"`
	Model          string `json:"model"`
	SerialNumber   string `json:"serial_number"`
	ProfileID      string `json:"profile_id"`
	NetworkAddress string `json:"network_address"`
	IsEnabled      *bool  `json:"is_enabled"`
}

func (s *Store) CreatePrinter(ctx context.Context, in NewPrinter) (*Printer, error) {
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, fmt.Errorf("打印机名称不能为空")
	}
	now := Now()
	p := Printer{
		ID:             "pr_" + uuid.NewString(),
		DisplayName:    strings.TrimSpace(in.DisplayName),
		Location:       strings.TrimSpace(in.Location),
		Vendor:         strings.TrimSpace(in.Vendor),
		Model:          strings.TrimSpace(in.Model),
		SerialNumber:   strings.TrimSpace(in.SerialNumber),
		ProfileID:      strings.TrimSpace(in.ProfileID),
		NetworkAddress: strings.TrimSpace(in.NetworkAddress),
		IsEnabled:      true,
		CreatedAt:      now,
		UpdatedAt:      now,
		Endpoints:      []Endpoint{},
		GroupIDs:       []string{},
	}
	if in.IsEnabled != nil {
		p.IsEnabled = *in.IsEnabled
	}
	var profile any
	if p.ProfileID != "" {
		profile = p.ProfileID
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO printers
		(id, display_name, location, vendor, model, serial_number, profile_id, network_address,
		is_enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.DisplayName, p.Location, p.Vendor, p.Model, p.SerialNumber, profile,
		p.NetworkAddress, boolInt(p.IsEnabled), p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) UpdatePrinter(ctx context.Context, id string, name, location, vendor, model, profile *string, enabled *bool) error {
	sets := []string{}
	args := []any{}
	if name != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, strings.TrimSpace(*name))
	}
	if location != nil {
		sets = append(sets, "location = ?")
		args = append(args, strings.TrimSpace(*location))
	}
	if vendor != nil {
		sets = append(sets, "vendor = ?")
		args = append(args, strings.TrimSpace(*vendor))
	}
	if model != nil {
		sets = append(sets, "model = ?")
		args = append(args, strings.TrimSpace(*model))
	}
	if profile != nil {
		sets = append(sets, "profile_id = ?")
		if strings.TrimSpace(*profile) == "" {
			args = append(args, nil)
		} else {
			args = append(args, strings.TrimSpace(*profile))
		}
	}
	if enabled != nil {
		sets = append(sets, "is_enabled = ?")
		args = append(args, boolInt(*enabled))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, Now(), id)
	res, err := s.DB.ExecContext(ctx, `UPDATE printers SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPrinterNetworkAddress records the LAN IP behind a printer even when the
// print path is a Windows helper (agent URIs carry no host).
func (s *Store) SetPrinterNetworkAddress(ctx context.Context, id, address string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE printers SET network_address = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(address), Now(), id)
	return err
}

// BackfillPrinterAddresses recovers the LAN address for printers added before
// network_address existed. Their Windows-helper endpoint has no host, but the
// audit entry written at add time still holds the IP the admin confirmed.
func (s *Store) BackfillPrinterAddresses(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM printers WHERE network_address = ''`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		rows, err := s.DB.QueryContext(ctx, `SELECT detail_json FROM audit_logs
			WHERE target_type = 'printer' AND target_id = ? ORDER BY id DESC LIMIT 20`, id)
		if err != nil {
			continue
		}
		host := ""
		for rows.Next() {
			var detail string
			if err := rows.Scan(&detail); err != nil {
				break
			}
			var d struct {
				URI string `json:"uri"`
			}
			if json.Unmarshal([]byte(detail), &d) != nil {
				continue
			}
			if h := uriHostname(d.URI); h != "" {
				host = h
				break
			}
		}
		rows.Close()
		if host == "" {
			continue
		}
		_ = s.SetPrinterNetworkAddress(ctx, id, host)
	}
	return nil
}

func uriHostname(raw string) string {
	rest := strings.TrimSpace(raw)
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	if strings.HasPrefix(rest, "[") {
		if j := strings.Index(rest, "]"); j > 0 {
			return rest[1:j]
		}
	}
	if i := strings.LastIndex(rest, ":"); i > 0 {
		rest = rest[:i]
	}
	return rest
}

func (s *Store) DeletePrinter(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Active jobs cannot outlive their queue, so fail them before detaching.
	// Finished history is kept: the printer name is snapshotted on the job and
	// the foreign key is ON DELETE SET NULL.
	if _, err := tx.ExecContext(ctx, `UPDATE print_jobs SET status = 'failed',
		error_code = 'PRINTER_DELETED', error_message = '打印机已被删除，请重新选择打印机',
		updated_at = ?, finished_at = ?
		WHERE printer_id = ? AND status IN ('queued', 'printing', 'waiting')`,
		Now(), Now(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE print_jobs SET endpoint_id = NULL,
		printer_id = NULL WHERE printer_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM printer_endpoint_events
		WHERE endpoint_id IN (SELECT id FROM printer_endpoints WHERE printer_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM printer_endpoints WHERE printer_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_printers WHERE printer_id = ?`, id); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM printers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// DisablePrinterEndpoints retires a printer's endpoints when the admin re-runs
// setup. Rows are kept because historic jobs reference them.
func (s *Store) DisablePrinterEndpoints(ctx context.Context, printerID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE printer_endpoints SET is_enabled = 0,
		last_error = '', updated_at = ? WHERE printer_id = ?`, Now(), printerID)
	return err
}

// EnablePrinterEndpoint reuses an IPP endpoint that already exists for this
// printer. Reconnecting a printer must not hit the global dedupe key or leave
// two endpoints pointing at the same device.
func (s *Store) EnablePrinterEndpoint(ctx context.Context, printerID, kind, uri string) (*Endpoint, error) {
	key := DedupeKey(kind, uri)
	row := s.DB.QueryRowContext(ctx, endpointSelect+` WHERE dedupe_key = ?`, key)
	ep, err := scanEndpoint(row)
	if err == nil {
		if ep.PrinterID != printerID {
			return nil, ErrConflict
		}
		_, err = s.DB.ExecContext(ctx, `UPDATE printer_endpoints SET is_enabled = 1,
			health_status = 'unknown', last_checked_at = NULL, last_success_at = NULL,
			last_error = '', updated_at = ? WHERE id = ?`, Now(), ep.ID)
		if err != nil {
			return nil, err
		}
		return s.GetEndpoint(ctx, ep.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return s.CreateEndpoint(ctx, printerID, NewEndpoint{Kind: kind, URI: uri, Priority: 100})
}

func (s *Store) ListAllEndpoints(ctx context.Context) ([]Endpoint, error) {
	rows, err := s.DB.QueryContext(ctx, endpointSelect+` ORDER BY printer_id, priority, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Endpoint{}
	for rows.Next() {
		ep, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

func (s *Store) ListEndpoints(ctx context.Context, printerID string) ([]Endpoint, error) {
	rows, err := s.DB.QueryContext(ctx, endpointSelect+` WHERE printer_id = ? ORDER BY priority, created_at`, printerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Endpoint{}
	for rows.Next() {
		ep, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

const endpointSelect = `SELECT id, printer_id, kind, uri, dedupe_key,
	queue_name, protocol, mac, serial_number, priority, is_enabled, health_status,
	COALESCE(last_checked_at, ''), COALESCE(last_success_at, ''), last_error,
	capabilities_json, capabilities_source FROM printer_endpoints`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEndpoint(row rowScanner) (Endpoint, error) {
	var ep Endpoint
	var caps string
	err := row.Scan(&ep.ID, &ep.PrinterID, &ep.Kind, &ep.URI, &ep.DedupeKey,
		&ep.QueueName, &ep.Protocol, &ep.MAC, &ep.SerialNumber, &ep.Priority,
		&ep.IsEnabled, &ep.Health, &ep.LastCheckedAt, &ep.LastSuccessAt, &ep.LastError,
		&caps, &ep.CapabilitySource)
	if err != nil {
		return ep, err
	}
	_ = json.Unmarshal([]byte(caps), &ep.Capabilities)
	ep.DirectPDF = ep.Capabilities.SupportsPDF()
	return ep, nil
}

// SetEndpointQueue records which CUPS queue and driver back an endpoint.
func (s *Store) SetEndpointQueue(ctx context.Context, id, uri, queue, ppd string) error {
	caps := Capabilities{
		DocumentFormats: []string{"application/pdf"},
		MakeModel:       ppd,
		CheckedAt:       Now(),
	}
	body, _ := json.Marshal(caps)
	_, err := s.DB.ExecContext(ctx, `UPDATE printer_endpoints SET uri = ?, queue_name = ?,
		capabilities_json = ?, updated_at = ? WHERE id = ?`,
		uri, queue, string(body), Now(), id)
	return err
}

func (s *Store) GetEndpoint(ctx context.Context, id string) (*Endpoint, error) {
	row := s.DB.QueryRowContext(ctx, endpointSelect+` WHERE id = ?`, id)
	ep, err := scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ep, nil
}

func (s *Store) GetEndpointByDedupe(ctx context.Context, key string) (*Endpoint, error) {
	row := s.DB.QueryRowContext(ctx, endpointSelect+` WHERE dedupe_key = ?`, key)
	ep, err := scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ep, nil
}

type NewEndpoint struct {
	Kind         string `json:"kind"`
	URI          string `json:"uri"`
	QueueName    string `json:"queue_name"`
	Protocol     string `json:"protocol"`
	MAC          string `json:"mac"`
	SerialNumber string `json:"serial_number"`
	Priority     int    `json:"priority"`
	IsEnabled    *bool  `json:"is_enabled"`
}

func DedupeKey(kind, uri string) string {
	return kind + "|" + strings.ToLower(strings.TrimSpace(uri))
}

func (s *Store) CreateEndpoint(ctx context.Context, printerID string, in NewEndpoint) (*Endpoint, error) {
	if in.Kind != "ipp" && in.Kind != "cups" {
		return nil, fmt.Errorf("端点类型必须是 ipp（打印机直连）或 cups（本机 CUPS 队列）")
	}
	uri := strings.TrimSpace(in.URI)
	if uri == "" {
		return nil, fmt.Errorf("端点地址不能为空")
	}
	if in.Priority <= 0 {
		in.Priority = 100
	}
	enabled := true
	if in.IsEnabled != nil {
		enabled = *in.IsEnabled
	}
	now := Now()
	ep := Endpoint{
		ID:               "ep_" + uuid.NewString(),
		PrinterID:        printerID,
		Kind:             in.Kind,
		URI:              uri,
		DedupeKey:        DedupeKey(in.Kind, uri),
		QueueName:        strings.TrimSpace(in.QueueName),
		Protocol:         strings.TrimSpace(in.Protocol),
		MAC:              strings.ToLower(strings.TrimSpace(in.MAC)),
		SerialNumber:     strings.TrimSpace(in.SerialNumber),
		Priority:         in.Priority,
		IsEnabled:        enabled,
		Health:           "unknown",
		CapabilitySource: "inferred",
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO printer_endpoints
		(id, printer_id, kind, uri, dedupe_key, queue_name, protocol, mac, serial_number,
		priority, is_enabled, health_status, capabilities_json, capabilities_source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unknown', '{}', 'inferred', ?, ?)`,
		ep.ID, ep.PrinterID, ep.Kind, ep.URI, ep.DedupeKey, ep.QueueName,
		ep.Protocol, ep.MAC, ep.SerialNumber, ep.Priority, boolInt(enabled), now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrConflict
		}
		return nil, err
	}
	ep.Capabilities = Capabilities{}
	return &ep, nil
}

func (s *Store) UpdateEndpoint(ctx context.Context, id string, priority *int, enabled *bool, uri *string) error {
	sets := []string{}
	args := []any{}
	if priority != nil {
		sets = append(sets, "priority = ?")
		args = append(args, *priority)
	}
	if enabled != nil {
		sets = append(sets, "is_enabled = ?")
		args = append(args, boolInt(*enabled))
	}
	if uri != nil {
		ep, err := s.GetEndpoint(ctx, id)
		if err != nil {
			return err
		}
		sets = append(sets, "uri = ?", "dedupe_key = ?")
		args = append(args, strings.TrimSpace(*uri), DedupeKey(ep.Kind, *uri))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, Now(), id)
	res, err := s.DB.ExecContext(ctx, `UPDATE printer_endpoints SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateEndpointHealth(ctx context.Context, id, health string, caps Capabilities, errMsg string) error {
	body, _ := json.Marshal(caps)
	now := Now()
	var success any
	if health == "healthy" {
		success = now
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE printer_endpoints SET health_status = ?,
		capabilities_json = ?, capabilities_source = 'probed', last_checked_at = ?,
		last_success_at = COALESCE(?, last_success_at), last_error = ?, updated_at = ?
		WHERE id = ?`, health, string(body), now, success, truncate(errMsg, 1000), now, id)
	return err
}

func (s *Store) RecordEndpointEvent(ctx context.Context, endpointID, event string, detail any) {
	b, _ := json.Marshal(detail)
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO printer_endpoint_events(endpoint_id, event, detail_json, created_at)
		VALUES (?, ?, ?, ?)`, endpointID, event, string(b), Now())
}

// ListEndpointEvents returns a printer's driver and device history, newest
// first. Events are keyed by endpoint, so a printer with a replaced queue
// still shows the old connection's history — that is usually the point.
//
// detail_json is decoded back into a map so callers get structured data
// instead of an escaped string; a malformed row is returned without detail
// rather than failing the whole list.
func (s *Store) ListEndpointEvents(ctx context.Context, printerID string, limit int, eventFilter string) ([]EndpointEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ev.id, ev.endpoint_id, ev.event, ev.detail_json, ev.created_at
		FROM printer_endpoint_events ev
		JOIN printer_endpoints ep ON ep.id = ev.endpoint_id
		WHERE ep.printer_id = ?`
	args := []any{printerID}
	if eventFilter != "" {
		q += ` AND ev.event = ?`
		args = append(args, eventFilter)
	}
	q += ` ORDER BY ev.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EndpointEvent{}
	for rows.Next() {
		var ev EndpointEvent
		var detail string
		if err := rows.Scan(&ev.ID, &ev.EndpointID, &ev.Event, &detail, &ev.CreatedAt); err != nil {
			return nil, err
		}
		var parsed any
		if json.Unmarshal([]byte(detail), &parsed) == nil {
			ev.Detail = parsed
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) ListCandidates(ctx context.Context, status string) ([]Candidate, error) {
	q := `SELECT id, source, discovered_uri, kind, host, port, path, dedupe_key, mac,
		vendor, model, serial_number, raw_json, status, possible_ip_change,
		COALESCE(merged_printer_id, ''), first_seen_at, last_seen_at
		FROM printer_candidates`
	args := []any{}
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY last_seen_at DESC LIMIT 500`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Candidate{}
	for rows.Next() {
		var c Candidate
		var raw string
		if err := rows.Scan(&c.ID, &c.Source, &c.DiscoveredURI, &c.Kind, &c.Host,
			&c.Port, &c.Path, &c.DedupeKey, &c.MAC, &c.Vendor, &c.Model,
			&c.SerialNumber, &raw, &c.Status, &c.PossibleIPChange,
			&c.MergedPrinterID, &c.FirstSeenAt, &c.LastSeenAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &c.Raw)
		if m, ok := c.Raw.(map[string]any); ok {
			if v, ok := m["print_directly"].(bool); ok {
				c.Direct = v
			}
			if v, ok := m["direct"].(bool); ok {
				c.Direct = v
			}
			if v, ok := m["probed"].(bool); ok {
				c.ProbeAttempted = v
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCandidate(ctx context.Context, id string) (*Candidate, error) {
	list, err := s.ListCandidates(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, ErrNotFound
}

type CandidateInput struct {
	Source        string
	DiscoveredURI string
	Kind          string
	Host          string
	Port          int
	Path          string
	MAC           string
	Vendor        string
	Model         string
	SerialNumber  string
	Raw           any
}

func (s *Store) UpsertCandidate(ctx context.Context, in CandidateInput) (string, bool, error) {
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = "ipp"
	}
	key := DedupeKey(kind, in.DiscoveredURI)
	now := Now()
	var id string
	var first string
	err := s.DB.QueryRowContext(ctx, `SELECT id, first_seen_at FROM printer_candidates WHERE dedupe_key = ?`, key).Scan(&id, &first)
	if err == nil {
		_, err = s.DB.ExecContext(ctx, `UPDATE printer_candidates SET last_seen_at = ?, source = ?,
			host = ?, port = ?, path = ?, mac = ?, vendor = ?, model = ?,
			serial_number = ?, raw_json = ? WHERE id = ?`,
			now, in.Source, in.Host, in.Port, in.Path, in.MAC, in.Vendor, in.Model,
			in.SerialNumber, jsonString(in.Raw), id)
		return id, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	possible := 0
	if strings.TrimSpace(in.MAC) != "" {
		var n int
		_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM printer_endpoints WHERE lower(mac) = ?`,
			strings.ToLower(strings.TrimSpace(in.MAC))).Scan(&n)
		if n > 0 {
			possible = 1
		}
	}
	id = "c_" + uuid.NewString()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO printer_candidates
		(id, source, discovered_uri, kind, host, port, path, dedupe_key, mac, vendor,
		model, serial_number, raw_json, status, possible_ip_change, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'new', ?, ?, ?)`,
		id, in.Source, in.DiscoveredURI, kind, in.Host, in.Port, in.Path, key,
		strings.ToLower(strings.TrimSpace(in.MAC)), in.Vendor, in.Model, in.SerialNumber,
		jsonString(in.Raw), possible, now, now)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (s *Store) SetCandidateStatus(ctx context.Context, id, status, printerID string) error {
	var merged any
	if printerID != "" {
		merged = printerID
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE printer_candidates SET status = ?, merged_printer_id = ? WHERE id = ?`,
		status, merged, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
