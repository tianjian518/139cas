package driver

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type CASPreviewNamer interface {
	CASPreviewName(ctx context.Context, file model.Obj) (string, error)
}

// CASSourceSizer 由支持 .cas 秒传元数据的驱动实现，
// 用于在文件列表中直接暴露 .cas 所对应的原始文件大小与名称。
type CASSourceSizer interface {
	CASSourceSize(ctx context.Context, file model.Obj) (size int64, name string, err error)
}

type CASDownloadRestoreController interface {
	CASDownloadRestoreEnabled() bool
}
