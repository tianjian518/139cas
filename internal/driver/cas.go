package driver

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type CASPreviewNamer interface {
	CASPreviewName(ctx context.Context, file model.Obj) (string, error)
}

type CASDownloadRestoreController interface {
	CASDownloadRestoreEnabled() bool
}
