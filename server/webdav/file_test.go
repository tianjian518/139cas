package webdav

import (
	"context"
	"reflect"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
)

func TestRenameMovedFileRefreshesDestinationAndRetries(t *testing.T) {
	oldRename := renameFileOp
	oldList := listFileOp
	oldDelay := moveRenameRetryDelay
	defer func() {
		renameFileOp = oldRename
		listFileOp = oldList
		moveRenameRetryDelay = oldDelay
	}()

	moveRenameRetryDelay = 0
	var calls []string
	attempts := 0
	renameFileOp = func(ctx context.Context, srcPath, dstName string, skipHook ...bool) error {
		attempts++
		calls = append(calls, "rename:"+srcPath+"->"+dstName)
		if attempts == 1 {
			return errors.WithStack(errs.ObjectNotFound)
		}
		return nil
	}
	listFileOp = func(ctx context.Context, path string, args *fs.ListArgs) ([]model.Obj, error) {
		if args == nil || !args.Refresh {
			t.Fatalf("expected refresh list args, got %#v", args)
		}
		calls = append(calls, "list:"+path)
		return nil, nil
	}

	err := renameMovedFile(context.Background(), "/library/show/Season 1", "source-name.mkv", "target-name.mkv")
	if err != nil {
		t.Fatalf("renameMovedFile returned error: %v", err)
	}

	want := []string{
		"rename:/library/show/Season 1/source-name.mkv->target-name.mkv",
		"list:/library/show/Season 1",
		"rename:/library/show/Season 1/source-name.mkv->target-name.mkv",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls mismatch:\n got %#v\nwant %#v", calls, want)
	}
}
