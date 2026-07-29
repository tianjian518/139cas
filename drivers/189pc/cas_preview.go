package _189pc

import (
	"context"
	"net/http"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

func (y *Cloud189PC) shouldPlayCAS(file model.Obj, args model.LinkArgs) bool {
	if !isCASName(file.GetName()) {
		return false
	}
	switch strings.ToLower(args.Type) {
	case "cas_video":
		return true
	default:
		return false
	}
}

func (y *Cloud189PC) CASDownloadRestoreEnabled() bool {
	return y.CASDownloadRestore
}

func (y *Cloud189PC) CASPreviewName(ctx context.Context, file model.Obj) (string, error) {
	if !isCASName(file.GetName()) {
		return file.GetName(), nil
	}
	info, err := y.parseCASFromObj(ctx, file)
	if err != nil {
		return "", err
	}
	previewName, err := resolveCASRestoreName(file.GetName(), info)
	if err != nil {
		return "", err
	}
	if !casmeta.ExtAllowed(previewName, y.CASExtAllowlist) {
		return file.GetName(), nil
	}
	if !y.CASDownloadRestore && !isVideoName(previewName) {
		return file.GetName(), nil
	}
	return previewName, nil
}

func (y *Cloud189PC) linkCASVideo(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	info, err := y.parseCASFromObj(ctx, file)
	if err != nil {
		return nil, err
	}
	previewName, err := resolveCASRestoreName(file.GetName(), info)
	if err != nil {
		return nil, err
	}
	if !casmeta.ExtAllowed(previewName, y.CASExtAllowlist) {
		return y.Link(ctx, file, model.LinkArgs{IP: args.IP, Header: args.Header, Type: "raw_cas", Redirect: args.Redirect})
	}
	if !y.CASDownloadRestore && !isVideoName(previewName) {
		return y.Link(ctx, file, model.LinkArgs{IP: args.IP, Header: args.Header, Type: "raw_cas", Redirect: args.Redirect})
	}
	y.beginCleanupTask()
	tempRoot, err := y.ensureTempDir(ctx)
	if err != nil {
		y.endCleanupTask()
		return nil, err
	}
	tempIsFamily := y.isFamily() || y.FamilyTransfer
	tempDir, err := y.createTempSubDir(ctx, tempRoot, casTempSubDirName(), tempIsFamily)
	if err != nil {
		y.endCleanupTask()
		return nil, err
	}

	restoreInfo := *info
	restoreInfo.Name = previewName
	tempObj, err := y.restoreCASDirect(ctx, tempDir, &restoreInfo, tempIsFamily, false)
	if err != nil {
		_ = y.Delete(context.TODO(), IF(tempIsFamily, y.FamilyID, ""), tempDir)
		y.endCleanupTask()
		return nil, err
	}
	link, err := y.linkObj(ctx, tempObj, model.LinkArgs{IP: args.IP, Header: args.Header, Type: "raw_video", Redirect: args.Redirect}, tempIsFamily)
	if err != nil {
		_ = y.Delete(context.TODO(), IF(tempIsFamily, y.FamilyID, ""), tempDir)
		y.endCleanupTask()
		return nil, err
	}
	go func() {
		if err := y.Delete(context.TODO(), IF(tempIsFamily, y.FamilyID, ""), tempDir); err != nil {
			utils.Log.Errorf("casPlayTempDeleteError:%s", err)
		}
		y.endCleanupTask()
	}()
	return link, nil
}

func (y *Cloud189PC) linkObj(ctx context.Context, file model.Obj, args model.LinkArgs, isFamily bool) (*model.Link, error) {
	var downloadUrl struct {
		URL string `json:"fileDownloadUrl"`
	}
	fullUrl := API_URL
	if isFamily {
		fullUrl += "/family/file"
	}
	fullUrl += "/getFileDownloadUrl.action"
	_, err := y.get(fullUrl, func(r *resty.Request) {
		r.SetContext(ctx)
		r.SetQueryParam("fileId", file.GetID())
		if isFamily {
			r.SetQueryParam("familyId", y.FamilyID)
		} else {
			r.SetQueryParams(map[string]string{
				"dt":   "3",
				"flag": "1",
			})
		}
	}, &downloadUrl, isFamily)
	if err != nil {
		return nil, err
	}
	downloadUrl.URL = strings.Replace(strings.ReplaceAll(downloadUrl.URL, "&amp;", "&"), "http://", "https://", 1)
	res, err := base.NoRedirectClient.R().SetContext(ctx).SetDoNotParseResponse(true).Get(downloadUrl.URL)
	if err != nil {
		return nil, err
	}
	defer res.RawBody().Close()
	if res.StatusCode() == 302 {
		downloadUrl.URL = res.Header().Get("location")
	}
	return &model.Link{
		URL: downloadUrl.URL,
		Header: http.Header{
			"User-Agent": []string{base.UserAgent},
		},
	}, nil
}

func isVideoName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv", ".ts", ".m2ts", ".wmv", ".rmvb", ".m4v", ".mpg", ".mpeg", ".3gp":
		return true
	default:
		return false
	}
}
