package store

import "time"

type User struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	PasswordHash       string `json:"-"`
	Role               string `json:"role"`
	DailyPageQuota     int    `json:"daily_page_quota"`
	IsActive           bool   `json:"is_active"`
	MustChangePassword bool   `json:"must_change_password"`
	CreatedAt          string `json:"created_at"`
}

type Profile struct {
	ID           string   `json:"id"`
	Vendor       string   `json:"vendor"`
	ModelPattern string   `json:"model_pattern"`
	Transports   []string `json:"transports"`
	Capabilities any      `json:"capabilities"`
	Notes        []string `json:"notes"`
	IsBuiltin    bool     `json:"is_builtin"`
}

type Capabilities struct {
	DocumentFormats []string `json:"document_formats,omitempty"`
	Sides           []string `json:"sides,omitempty"`
	Media           []string `json:"media,omitempty"`
	ColorModes      []string `json:"color_modes,omitempty"`
	MaxCopies       int      `json:"max_copies,omitempty"`
	MakeModel       string   `json:"make_model,omitempty"`
	State           string   `json:"state,omitempty"`
	StateReasons    []string `json:"state_reasons,omitempty"`
	CheckedAt       string   `json:"checked_at,omitempty"`
}

func (c Capabilities) SupportsPDF() bool {
	for _, f := range c.DocumentFormats {
		// application/octet-stream only means "some binary", not "PDF". Many
		// printers advertise it while accepting only PWG/URF raster. Treating
		// that as PDF support makes jobs vanish into the device.
		if f == "application/pdf" || f == "*/*" {
			return true
		}
	}
	return false
}

type Endpoint struct {
	ID               string       `json:"id"`
	PrinterID        string       `json:"printer_id"`
	Kind             string       `json:"kind"`
	URI              string       `json:"uri"`
	DedupeKey        string       `json:"dedupe_key"`
	AgentID          string       `json:"agent_id,omitempty"`
	QueueName        string       `json:"queue_name,omitempty"`
	Protocol         string       `json:"protocol,omitempty"`
	MAC              string       `json:"mac,omitempty"`
	SerialNumber     string       `json:"serial_number,omitempty"`
	Priority         int          `json:"priority"`
	IsEnabled        bool         `json:"is_enabled"`
	Health           string       `json:"health_status"`
	LastCheckedAt    string       `json:"last_checked_at,omitempty"`
	LastSuccessAt    string       `json:"last_success_at,omitempty"`
	LastError        string       `json:"last_error,omitempty"`
	Capabilities     Capabilities `json:"capabilities"`
	CapabilitySource string       `json:"capabilities_source"`
	AgentName        string       `json:"agent_name,omitempty"`
}

type Printer struct {
	ID           string     `json:"id"`
	DisplayName  string     `json:"display_name"`
	Location     string     `json:"location"`
	Vendor       string     `json:"vendor"`
	Model        string     `json:"model"`
	SerialNumber string     `json:"serial_number"`
	ProfileID    string     `json:"profile_id"`
	// NetworkAddress is the printer's LAN IP, kept even when the job is sent
	// through a Windows helper. It is how discovery knows the device is
	// already registered.
	NetworkAddress string   `json:"network_address"`
	IsEnabled    bool       `json:"is_enabled"`
	Endpoints    []Endpoint `json:"endpoints"`
	GroupIDs     []string   `json:"group_ids"`
	CreatedAt    string     `json:"created_at"`
	UpdatedAt    string     `json:"updated_at"`
}

type Group struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	MemberCount int         `json:"member_count"`
	PrinterIDs  []string    `json:"printer_ids"`
	Members     []GroupUser `json:"members,omitempty"`
	CreatedAt   string      `json:"created_at"`
	UpdatedAt   string      `json:"updated_at"`
}

type GroupUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	IsActive    bool   `json:"is_active"`
}

type Candidate struct {
	ID               string `json:"id"`
	Source           string `json:"source"`
	DiscoveredURI    string `json:"discovered_uri"`
	Kind             string `json:"kind"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Path             string `json:"path"`
	DedupeKey        string `json:"dedupe_key"`
	MAC              string `json:"mac,omitempty"`
	Vendor           string `json:"vendor,omitempty"`
	Model            string `json:"model,omitempty"`
	SerialNumber     string `json:"serial_number,omitempty"`
	Raw              any    `json:"raw_json,omitempty"`
	Direct           bool   `json:"direct"`
	NeedsAgent       bool   `json:"needs_agent"`
	// ProbeAttempted separates "we asked and it cannot take PDF" from "we
	// could not reach it at all". The UI must not tell a user to install a
	// helper on another PC just because the printer was switched off.
	ProbeAttempted bool `json:"probe_attempted"`
	Status           string `json:"status"`
	PossibleIPChange bool   `json:"possible_ip_change"`
	MergedPrinterID  string `json:"merged_printer_id,omitempty"`
	// AlreadyAdded is filled in by the API, not stored. It lets the page hide
	// devices that are already registered even when the endpoint is a Windows
	// helper URI and therefore has no IP to compare against.
	AlreadyAdded bool   `json:"already_added"`
	PrinterName  string `json:"printer_name,omitempty"`
	FirstSeenAt      string `json:"first_seen_at"`
	LastSeenAt       string `json:"last_seen_at"`
}

type StoredFile struct {
	ID           string `json:"id"`
	OwnerID      string `json:"owner_id"`
	Kind         string `json:"kind"`
	OriginalName string `json:"original_name"`
	MIME         string `json:"mime"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	StoragePath  string `json:"-"`
	PageCount    int    `json:"page_count"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type Job struct {
	ID            string `json:"id"`
	UserID        string `json:"user_id"`
	Username      string `json:"username,omitempty"`
	PrinterID     string `json:"printer_id"`
	PrinterName   string `json:"printer_name,omitempty"`
	EndpointID    string `json:"endpoint_id,omitempty"`
	EndpointURI   string `json:"endpoint_uri,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	OriginalFile  string `json:"original_file_id,omitempty"`
	PDFFile       string `json:"pdf_file_id,omitempty"`
	FileName      string `json:"file_name,omitempty"`
	Status        string `json:"status"`
	Copies        int    `json:"copies"`
	Duplex        string `json:"duplex"`
	ColorMode     string `json:"color_mode"`
	PaperSize     string `json:"paper_size"`
	PageCount     int    `json:"page_count"`
	ErrorCode     string `json:"error_code,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
	RetryOf       string `json:"retry_of,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	ClaimedAt     string `json:"claimed_at,omitempty"`
	LeaseExpires  string `json:"lease_expires_at,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
	TotalPages    int    `json:"total_pages"`
	AgentEndpoint bool   `json:"-"`
	PDFPath       string `json:"-"`
	QueueName     string `json:"queue_name,omitempty"`
}

type Agent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	PrintCommand string   `json:"print_command"`
	LastSeenAt   string   `json:"last_seen_at,omitempty"`
	Queues       any      `json:"queues"`
	Status       any      `json:"status"`
	IsActive     bool     `json:"is_active"`
	CreatedAt    string   `json:"created_at"`
	Online       bool     `json:"online"`
	EndpointIDs  []string `json:"endpoint_ids,omitempty"`
	PrinterIDs   []string `json:"printer_ids,omitempty"`
}

type DiscoveryRun struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	CIDR         string `json:"cidr,omitempty"`
	Status       string `json:"status"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at,omitempty"`
	FoundCount   int    `json:"found_count"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type DirectoryUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type DailyUsage struct {
	Pages int `json:"pages"`
	Quota int `json:"quota"`
}

type Dashboard struct {
	PrintersEnabled int            `json:"printers_enabled"`
	PrintersTotal   int            `json:"printers_total"`
	QueuedJobs      int            `json:"queued_jobs"`
	PrintingJobs    int            `json:"printing_jobs"`
	FailedJobsToday int            `json:"failed_jobs_today"`
	CompletedToday  int            `json:"completed_today"`
	NewCandidates   int            `json:"new_candidates"`
	AgentsOnline    int            `json:"agents_online"`
	AgentsTotal     int            `json:"agents_total"`
	RecentJobs      []Job          `json:"recent_jobs"`
	StatusCounts    map[string]int `json:"status_counts"`
	DailyPages      int            `json:"daily_pages"`
	DailyPageQuota  int            `json:"daily_page_quota"`
	MaxUploadMB     int64          `json:"max_upload_mb"`
}

type JobFilter struct {
	UserID   string
	Statuses []string
	Limit    int
	Offset   int
}

func Now() string { return time.Now().UTC().Format(time.RFC3339) }

func ParseTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339, v)
	return t
}
