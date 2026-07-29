package guangyapan

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Object struct {
	model.Object
	ParentID string
	DriveID  string
}

type apiResponse map[string]any

func (o *Object) ModTime() time.Time {
	return o.Object.ModTime()
}

var _ model.Obj = (*Object)(nil)
