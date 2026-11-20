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
	// Resolution is the device's native raster DPI as reported by
	// printer-resolution-default. Rendering at the same resolution keeps the
	// printer from resampling (and from rejecting a mismatched raster).
	Resolution int `json:"resolution,omitempty"`
}

// ResolutionDPI returns the DPI to render at, defaulting to 600 because every
// driverless monochrome laser seen so far advertises that.
func (c Capabilities) ResolutionDPI() int {
	if c.Resolution >= 150 && c.Resolution <= 1200 {
		return c.Resolution
	}
	return 600
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
	// QueueName is the CUPS queue behind a "cups" endpoint. For "ipp"
	// endpoints it is empty and URI is the device URL.
	QueueName string `json:"queue_name,omitempty"`
	// PPDName is the driver CUPS uses for a queue, shown so an admin can see
	// whether the vendor driver was picked instead of a generic fallback.
	PPDName string `json:"ppd_name,omitempty"`
	// DirectPDF is true when the device itself accepts application/pdf.
	DirectPDF bool   `json:"direct_pdf,omitempty"`
	Jammed    string `json:"jammed,omitempty"`
}

type Printer struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name"`
	Location     string `json:"location"`
	Vendor       string `json:"vendor"`
	Model        string `json:"model"`
	SerialNumber string `json:"serial_number"`
	ProfileID    string `json:"profile_id"`
	// NetworkAddress is the printer's LAN IP, kept even when the job is sent
	// through a Windows helper. It is how discovery knows the device is
	// already registered.
	NetworkAddress string     `json:"network_address"`
	IsEnabled      bool       `json:"is_enabled"`
	Endpoints      []Endpoint `json:"endpoints"`
	GroupIDs       []string   `json:"group_ids"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      string     `json:"updated_at"`
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
	ID            string `json:"id"`
	Source        string `json:"source"`
	DiscoveredURI string `json:"discovered_uri"`
	Kind          string `json:"kind"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Path          string `json:"path"`
	DedupeKey     string `json:"dedupe_key"`
	MAC           string `json:"mac,omitempty"`
	Vendor        string `json:"vendor,omitempty"`
	Model         string `json:"model,omitempty"`
	SerialNumber  string `json:"serial_number,omitempty"`
	Raw           any    `json:"raw_json,omitempty"`
	Direct        bool   `json:"direct"`
	// ProbeAttempted separates "we asked and it cannot take PDF" from "we
	// could not reach it at all". A device that is merely switched off must
	// not be reported as missing a driver.
	ProbeAttempted   bool   `json:"probe_attempted"`
	Status           string `json:"status"`
	PossibleIPChange bool   `json:"possible_ip_change"`
	MergedPrinterID  string `json:"merged_printer_id,omitempty"`
	// AlreadyAdded is filled in by the API, not stored. It lets the page hide
	// devices that are already registered.
	AlreadyAdded bool   `json:"already_added"`
	PrinterName  string `json:"printer_name,omitempty"`
	FirstSeenAt  string `json:"first_seen_at"`
	LastSeenAt   string `json:"last_seen_at"`
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
	ID           string `json:"id"`
	UserID       string `json:"user_id"`
	Username     string `json:"username,omitempty"`
	PrinterID    string `json:"printer_id"`
	PrinterName  string `json:"printer_name,omitempty"`
	EndpointID   string `json:"endpoint_id,omitempty"`
	EndpointURI  string `json:"endpoint_uri,omitempty"`
	OriginalFile string `json:"original_file_id,omitempty"`
	PDFFile      string `json:"pdf_file_id,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	Status       string `json:"status"`
	Copies       int    `json:"copies"`
	Duplex       string `json:"duplex"`
	ColorMode    string `json:"color_mode"`
	PaperSize    string `json:"paper_size"`
	PageRange    string `json:"page_range,omitempty"`
	PageCount    int    `json:"page_count"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	RetryOf      string `json:"retry_of,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	ClaimedAt    string `json:"claimed_at,omitempty"`
	LeaseExpires string `json:"lease_expires_at,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
	TotalPages   int    `json:"total_pages"`
	PDFPath      string `json:"-"`
	QueueName    string `json:"queue_name,omitempty"`
	CUPSJobID    string `json:"cups_job_id,omitempty"`
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

// EndpointEvent is one driver/probe record for a printer connection. The
// queue shows jobs, but "the driver worked yesterday and fails today" is only
// visible here, so it is exposed as its own read-only history.
type EndpointEvent struct {
	ID         int64  `json:"id"`
	EndpointID string `json:"endpoint_id"`
	Event      string `json:"event"`
	Detail     any    `json:"detail,omitempty"`
	CreatedAt  string `json:"created_at"`
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
	CUPSReady       int            `json:"cups_ready"`
	CUPSTotal       int            `json:"cups_total"`
	RecentJobs      []Job          `json:"recent_jobs"`
	StatusCounts    map[string]int `json:"status_counts"`
	DailyPages      int            `json:"daily_pages"`
	DailyPageQuota  int            `json:"daily_page_quota"`
	MaxUploadMB     int64          `json:"max_upload_mb"`
}

type JobFilter struct {
	UserID    string
	PrinterID string
	Statuses  []string
	Limit     int
	Offset    int
}

func Now() string { return time.Now().UTC().Format(time.RFC3339) }

func ParseTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339, v)
	return t
}
