package webapi

import (
	"context"

	"cloudprint/internal/store"
)

func toSession(se *store.Session) *store.Session { return se }

var _ = context.Background
