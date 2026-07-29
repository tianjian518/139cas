package _189pc

import (
	"context"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func (y *Cloud189PC) beginAutoRestore(path string) bool {
	_, loaded := y.autoRestoreInFlight.LoadOrStore(path, struct{}{})
	return !loaded
}

func (y *Cloud189PC) endAutoRestore(path string) {
	y.autoRestoreInFlight.Delete(path)
}

func (y *Cloud189PC) autoRestoreCAS(ctx context.Context) error {
	if !y.autoRestoreMu.TryLock() {
		return nil
	}
	defer y.autoRestoreMu.Unlock()
	for _, p := range strings.Split(y.AutoRestoreExistingCASPaths, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		dir, err := y.getDirByPath(ctx, p, y.isFamily())
		if err != nil {
			utils.Log.Errorf("autoRestoreCASPathError:%s:%s", p, err)
			continue
		}
		if err = y.restoreCASInDir(ctx, dir, utils.FixAndCleanPath(p)); err != nil {
			utils.Log.Errorf("autoRestoreCASDirError:%s:%s", p, err)
		}
	}
	return nil
}

func (y *Cloud189PC) manualRefreshAutoRestorePath(args model.ListArgs) (string, bool) {
	if !args.Refresh || args.ReqPath == "" || !y.AutoRestoreExistingCAS || strings.TrimSpace(y.AutoRestoreExistingCASPaths) == "" {
		return "", false
	}
	reqPath := utils.FixAndCleanPath(strings.TrimSpace(args.ReqPath))
	mountPath := utils.GetActualMountPath(y.GetStorage().MountPath)
	if mountPath == "" {
		mountPath = "/"
	}
	if reqPath != mountPath && !strings.HasPrefix(reqPath, strings.TrimSuffix(mountPath, "/")+"/") {
		return "", false
	}
	dirPath := utils.FixAndCleanPath(strings.TrimPrefix(reqPath, mountPath))
	if y.autoRestorePathMonitored(dirPath) {
		return dirPath, true
	}
	return "", false
}

func (y *Cloud189PC) autoRestorePathMonitored(dirPath string) bool {
	dirPath = utils.FixAndCleanPath(dirPath)
	for _, p := range strings.Split(y.AutoRestoreExistingCASPaths, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		monitorPath := utils.FixAndCleanPath(p)
		if monitorPath == "/" || dirPath == monitorPath || strings.HasPrefix(dirPath, strings.TrimSuffix(monitorPath, "/")+"/") {
			return true
		}
	}
	return false
}

func (y *Cloud189PC) startManualRefreshAutoRestore(ctx context.Context, dir model.Obj, dirPath string) {
	go func() {
		if err := y.restoreCASInCurrentDir(context.WithoutCancel(ctx), dir, dirPath); err != nil {
			utils.Log.Errorf("manualRefreshAutoRestoreCASError:%s:%s", dirPath, err)
		}
	}()
}

func (y *Cloud189PC) restoreCASInCurrentDir(ctx context.Context, dir model.Obj, dirPath string) error {
	files, err := y.getFiles(ctx, dir.GetID(), y.isFamily())
	if err != nil {
		return err
	}
	for _, obj := range files {
		if obj.IsDir() || !isCASName(obj.GetName()) {
			continue
		}
		y.restoreCASObj(ctx, dir, utils.FixAndCleanPath(dirPath), obj)
	}
	return nil
}

func (y *Cloud189PC) restoreCASInDir(ctx context.Context, dir model.Obj, dirPath string) error {
	files, err := y.getFiles(ctx, dir.GetID(), y.isFamily())
	if err != nil {
		return err
	}
	for _, obj := range files {
		if obj.IsDir() {
			if err := y.restoreCASInDir(ctx, obj, path.Join(dirPath, obj.GetName())); err != nil {
				utils.Log.Errorf("autoRestoreCASSubDirError:%s:%s", obj.GetName(), err)
			}
			continue
		}
		if !isCASName(obj.GetName()) {
			continue
		}
		y.restoreCASObj(ctx, dir, dirPath, obj)
	}
	return nil
}

func (y *Cloud189PC) restoreCASObj(ctx context.Context, dir model.Obj, dirPath string, obj model.Obj) {
	casPath := path.Join(dirPath, obj.GetName())
	if !y.beginAutoRestore(casPath) {
		return
	}
	defer y.endAutoRestore(casPath)
	info, err := y.parseCASFromObj(ctx, obj)
	if err != nil {
		utils.Log.Errorf("autoRestoreCASParseError:%s:%s", obj.GetName(), err)
		return
	}
	restoredName, err := resolveCASRestoreName(obj.GetName(), info)
	if err != nil {
		utils.Log.Errorf("autoRestoreCASNameError:%s:%s", obj.GetName(), err)
		return
	}
	if !casmeta.ExtAllowed(restoredName, y.CASExtAllowlist) {
		return
	}
	if _, err = y.findFileByName(ctx, restoredName, dir.GetID(), y.isFamily()); err == nil {
		if y.DeleteCASAfterRestore {
			if err = y.Delete(ctx, IF(y.isFamily(), y.FamilyID, ""), obj); err != nil {
				utils.Log.Errorf("autoRestoreCASDeleteExistingError:%s:%s", obj.GetName(), err)
				return
			}
			op.Cache.DeleteDirectory(y, dirPath)
			y.notifyTaskDone()
		}
		return
	}
	if _, err = y.restoreCAS(ctx, dir, info, obj.GetName(), false); err != nil {
		utils.Log.Errorf("autoRestoreCASError:%s:%s", obj.GetName(), err)
		return
	}
	if y.DeleteCASAfterRestore {
		if err = y.Delete(ctx, IF(y.isFamily(), y.FamilyID, ""), obj); err != nil {
			utils.Log.Errorf("autoRestoreCASDeleteError:%s:%s", obj.GetName(), err)
			return
		}
	}
	op.Cache.DeleteDirectory(y, dirPath)
	y.notifyTaskDone()
}

func (y *Cloud189PC) getDirByPath(ctx context.Context, dirPath string, isFamily bool) (model.Obj, error) {
	dirPath = strings.Trim(dirPath, "/")
	if dirPath == "" {
		return &Cloud189Folder{ID: String(IF(isFamily, "", y.RootFolderID)), Name: "/"}, nil
	}
	current := &Cloud189Folder{ID: String(IF(isFamily, "", y.RootFolderID)), Name: "/"}
	for _, part := range strings.Split(dirPath, "/") {
		if part == "" {
			continue
		}
		children, err := y.getFiles(ctx, current.GetID(), isFamily)
		if err != nil {
			return nil, err
		}
		found := false
		for _, child := range children {
			if child.IsDir() && child.GetName() == part {
				if folder, ok := child.(*Cloud189Folder); ok {
					current = folder
				} else {
					current = &Cloud189Folder{ID: String(child.GetID()), Name: child.GetName()}
				}
				found = true
				break
			}
		}
		if !found {
			return nil, errs.ObjectNotFound
		}
	}
	return current, nil
}
