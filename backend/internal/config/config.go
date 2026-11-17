package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr                 string
	DataDir              string
	DBPath               string
	PublicURL            string
	AdminUsername        string
	AdminPassword        string
	AdminDisplayName     string
	CookieSecure         bool
	MaxUploadMB          int64
	LibreOffice          string
	PDFToPPM             string
	EnableMDNS           bool
	ScanCIDRs            []string
	SessionHours         int
	DefaultDailyQuota    int
	RetentionDays        int
	AllowPrivateScan     bool
	TrustedProxyHeaders  bool
	BootstrapGeneratedPW bool
}

func Load() Config {
	dataDir := env("CLOUDPRINT_DATA", "data")
	absData, err := filepath.Abs(dataDir)
	if err == nil {
		dataDir = absData
	}
	c := Config{
		Addr:              env("CLOUDPRINT_ADDR", ":8080"),
		DataDir:           dataDir,
		DBPath:            env("CLOUDPRINT_DB", filepath.Join(dataDir, "cloudprint.db")),
		PublicURL:         strings.TrimRight(env("CLOUDPRINT_PUBLIC_URL", ""), "/"),
		AdminUsername:     env("CLOUDPRINT_ADMIN_USER", "admin"),
		AdminPassword:     os.Getenv("CLOUDPRINT_ADMIN_PASSWORD"),
		AdminDisplayName:  env("CLOUDPRINT_ADMIN_DISPLAY", "管理员"),
		CookieSecure:      envBool("CLOUDPRINT_COOKIE_SECURE", false),
		MaxUploadMB:       envInt64("CLOUDPRINT_MAX_UPLOAD_MB", 50),
		LibreOffice:       os.Getenv("CLOUDPRINT_LIBREOFFICE"),
		PDFToPPM:          os.Getenv("CLOUDPRINT_PDFTOPPM"),
		EnableMDNS:        envBool("CLOUDPRINT_ENABLE_MDNS", true),
		ScanCIDRs:         splitList(os.Getenv("CLOUDPRINT_SCAN_CIDRS")),
		SessionHours:      int(envInt64("CLOUDPRINT_SESSION_HOURS", 12)),
		DefaultDailyQuota: int(envInt64("CLOUDPRINT_DAILY_QUOTA", 50)),
		RetentionDays:     int(envInt64("CLOUDPRINT_RETENTION_DAYS", 7)),
		AllowPrivateScan:  envBool("CLOUDPRINT_ALLOW_PRIVATE_SCAN", true),
		TrustedProxyHeaders: envBool(
			"CLOUDPRINT_TRUST_PROXY_HEADERS",
			false,
		),
	}
	if c.AdminPassword == "" {
		c.AdminPassword = randomPassword(18)
		c.BootstrapGeneratedPW = true
	}
	if c.MaxUploadMB < 1 {
		c.MaxUploadMB = 1
	}
	return c
}

func (c Config) UploadBytes() int64 { return c.MaxUploadMB << 20 }

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt64(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func randomPassword(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "change-me-" + hex.EncodeToString([]byte{1, 2, 3, 4})
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}
