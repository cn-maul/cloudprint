package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cloudprint/internal/auth"
	"cloudprint/internal/config"
	"cloudprint/internal/store"

	"github.com/google/uuid"
)

func Seed(ctx context.Context, st *store.Store, cfg config.Config) (string, error) {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	generated := ""
	if n == 0 {
		hash, err := auth.HashPassword(cfg.AdminPassword)
		if err != nil {
			return "", fmt.Errorf("bootstrap admin password: %w", err)
		}
		_, err = st.CreateUser(ctx, cfg.AdminUsername, cfg.AdminDisplayName, hash, "admin", 0)
		if err != nil {
			return "", fmt.Errorf("create bootstrap admin: %w", err)
		}
		if cfg.BootstrapGeneratedPW {
			generated = cfg.AdminPassword
		}
	}
	if err := seedProfiles(ctx, st); err != nil {
		return generated, err
	}
	return generated, nil
}

func seedProfiles(ctx context.Context, st *store.Store) error {
	now := store.Now()
	caps := map[string]any{
		"paper_sizes":  []string{"A4"},
		"duplex_modes": []string{"one-sided", "two-sided-long-edge"},
		"color_modes":  []string{"monochrome"},
	}
	b, _ := json.Marshal(caps)
	notes := []string{
		"Windows WSD/IPP Class Driver 队列可能出现 Error, Printing, Retained，PagesPrinted=0。",
		"已现场确认：Windows 原生奔图驱动 + RAW 9100 可正常出纸。",
		"IPP 直打 PDF 能力必须逐台探测，不能按型号推断。",
		"本档案不包含 IP 地址；端点由管理员添加或发现后审批。",
	}
	nb, _ := json.Marshal(notes)
	_, err := st.DB.ExecContext(ctx, `INSERT INTO printer_profiles
		(id, vendor, model_pattern, transports_json, capabilities_json, notes, is_builtin, created_at)
		VALUES ('pantum-ms6000nw', 'Pantum', 'MS6000NW*', '["ipp","socket","agent"]', ?, ?, 1, ?)
		ON CONFLICT(id) DO NOTHING`, string(b), string(nb), now)
	if err != nil {
		return fmt.Errorf("seed printer profile: %w", err)
	}
	_, err = st.DB.ExecContext(ctx, `INSERT INTO printer_profiles
		(id, vendor, model_pattern, transports_json, capabilities_json, notes, is_builtin, created_at)
		VALUES ('generic-ipp-pdf', '', '*', '["ipp","agent"]', '{"paper_sizes":["A4"],"duplex_modes":["one-sided","two-sided-long-edge","two-sided-short-edge"],"color_modes":["monochrome","color"]}', '["通用 IPP 档案：实际能力以端点探测为准。"]', 1, ?)
		ON CONFLICT(id) DO NOTHING`, now)
	if err != nil {
		return fmt.Errorf("seed generic profile: %w", err)
	}
	return nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

var _ = uuid.NewString
var _ = time.Now
