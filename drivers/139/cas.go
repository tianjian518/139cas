package _139

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

const casTempDirName = "TEMP"

type casUploadInfo = casmeta.Info

func isCASName(name string) bool {
	return casmeta.IsName(name)
}

func (d *Yun139) shouldUploadCAS(name string) bool {
	return d.GenerateCAS && !isCASName(name) && casmeta.ExtAllowed(name, d.CASExtAllowlist)
}

func (d *Yun139) shouldDeleteSource() bool {
	return d.GenerateCAS && d.DeleteSource
}

func (d *Yun139) CASDownloadRestoreEnabled() bool {
	return d.CASDownloadRestore
}

func (d *Yun139) shouldPlayCAS(file model.Obj, args model.LinkArgs) bool {
	if d.Addition.Type != MetaPersonalNew || !isCASName(file.GetName()) {
		return false
	}
	return strings.EqualFold(args.Type, "cas_video")
}

func (d *Yun139) CASPreviewName(ctx context.Context, file model.Obj) (string, error) {
	if !isCASName(file.GetName()) {
		return file.GetName(), nil
	}
	info, err := d.parseCASFromObj(ctx, file)
	if err != nil {
		return "", err
	}
	previewName, err := resolveCASRestoreName(file.GetName(), info)
	if err != nil {
		return "", err
	}
	if !casmeta.ExtAllowed(previewName, d.CASExtAllowlist) {
		return file.GetName(), nil
	}
	if !d.CASDownloadRestore && !isVideoName(previewName) {
		return file.GetName(), nil
	}
	return previewName, nil
}

func (d *Yun139) prepareCASPut(ctx context.Context, dstDir model.Obj, file model.FileStreamer) (model.FileStreamer, model.Obj, bool, error) {
	if !d.RestoreSourceFromCAS || !isCASName(file.GetName()) {
		return file, nil, false, nil
	}
	casData, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, true, err
	}
	info, err := d.parseCAS(casData)
	if err != nil {
		return nil, nil, true, err
	}
	restoredName, err := resolveCASRestoreName(file.GetName(), info)
	if err != nil {
		return nil, nil, true, err
	}
	if !casmeta.ExtAllowed(restoredName, d.CASExtAllowlist) {
		return &stream.FileStream{
			Ctx:               ctx,
			Obj:               file,
			Reader:            bytes.NewReader(casData),
			Mimetype:          file.GetMimetype(),
			ForceStreamUpload: file.IsForceStreamUpload(),
			Exist:             file.GetExist(),
		}, nil, false, nil
	}
	restored, err := d.restoreCAS(ctx, dstDir, info, file.GetName(), false)
	return nil, restored, true, err
}

func (d *Yun139) uploadCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo) (model.Obj, error) {
	if info == nil || !d.shouldUploadCAS(info.Name) {
		return nil, nil
	}
	content, err := casmeta.Encode(info)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	casObj := &model.Object{
		Name:     casmeta.FileName(info.Name),
		Size:     int64(len(content)),
		Modified: now,
		Ctime:    now,
		HashInfo: utils.NewHashInfo(utils.MD5, utils.HashData(utils.MD5, content)),
	}
	casStream := &stream.FileStream{
		Ctx:      ctx,
		Obj:      casObj,
		Reader:   bytes.NewReader(content),
		Mimetype: "text/plain",
	}
	uploadedCASObj, _, err := d.personalPut(ctx, dstDir, casStream, func(float64) {})
	if err != nil {
		return nil, err
	}
	if uploadedCASObj != nil {
		return uploadedCASObj, nil
	}
	return casObj, nil
}

func (d *Yun139) deleteSource(ctx context.Context, dstDir model.Obj, uploadedObj model.Obj, info *casUploadInfo) error {
	if info == nil || !d.shouldDeleteSource() || !d.shouldUploadCAS(info.Name) {
		return nil
	}
	if uploadedObj == nil || uploadedObj.GetID() == "" {
		var err error
		uploadedObj, err = d.findPersonalFileByName(ctx, info.Name, dstDir.GetID())
		if err != nil {
			return err
		}
	}
	return d.deletePersonalPermanently(ctx, uploadedObj)
}

func (d *Yun139) parseCAS(data []byte) (*casUploadInfo, error) {
	return casmeta.Decode(data)
}

func (d *Yun139) parseCASFromObj(ctx context.Context, file model.Obj) (*casUploadInfo, error) {
	link, err := d.Link(ctx, file, model.LinkArgs{Type: "raw_cas"})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	if link.URL == "" {
		return nil, fmt.Errorf("cas link has no url")
	}
	req := base.RestyClient.R().SetContext(ctx)
	if link.Header != nil {
		req.SetHeaders(headerToMap(link.Header))
	}
	resp, err := req.Get(link.URL)
	if err != nil {
		return nil, err
	}
	return d.parseCAS(resp.Body())
}

func resolveCASRestoreName(casName string, info *casUploadInfo) (string, error) {
	return casmeta.ResolveRestoreName(casName, info)
}

func (d *Yun139) shouldRestoreCASWithPCHeaders() bool {
	if d.CASRestoreUsePCHeaders {
		return true
	}
	raw := strings.TrimSpace(d.Storage.Addition)
	if raw == "" {
		return true
	}
	var addition map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &addition); err != nil {
		return true
	}
	value, ok := addition["cas_restore_use_pc_headers"]
	if !ok {
		return true
	}
	var enabled bool
	if err := json.Unmarshal(value, &enabled); err != nil {
		return true
	}
	return enabled
}

func (d *Yun139) applyCASRestorePCHeadersDefault() {
	d.CASRestoreUsePCHeaders = d.shouldRestoreCASWithPCHeaders()
}

func (d *Yun139) createCASRestoreBySHA256(ctx context.Context, dstDir model.Obj, name string, size int64, fullHash string) (*PersonalUploadResp, error) {
	if d.shouldRestoreCASWithPCHeaders() {
		resp, _, err := d.pcPersonalCreateBySHA256(ctx, dstDir, name, size, fullHash, "auto_rename")
		if err == nil {
			return resp, nil
		}
		if utils.IsCanceled(ctx) {
			return nil, err
		}
		utils.Log.Warnf("139yun: CAS restore PC headers create failed for %q, falling back to web headers: %v", name, err)
	}
	resp, _, err := d.personalCreateBySHA256(ctx, dstDir, name, size, fullHash, "auto_rename")
	return resp, err
}

func (d *Yun139) restoreCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo, casName string, temp bool) (model.Obj, error) {
	targetName, err := resolveCASRestoreName(casName, info)
	if err != nil {
		return nil, err
	}
	if !casmeta.ExtAllowed(targetName, d.CASExtAllowlist) {
		return nil, fmt.Errorf("cas restore skipped: extension of %q is not allowed", targetName)
	}
	if info.SHA256 == "" {
		return nil, fmt.Errorf("cas restore failed: missing sha256")
	}
	if temp {
		targetName = fmt.Sprintf("TEMP_%d_%s_%s", time.Now().UnixNano()/1e6, randomSuffix(), targetName)
	}
	if existing, err := d.findPersonalFileByName(ctx, targetName, dstDir.GetID()); err == nil && !temp {
		return existing, nil
	}
	resp, err := d.createCASRestoreBySHA256(ctx, dstDir, targetName, info.Size, info.SHA256)
	if err != nil {
		return nil, err
	}
	if !resp.Data.Exist && !resp.Data.RapidUpload && resp.Data.PartInfos != nil {
		return nil, fmt.Errorf("cas restore failed: source file data does not exist in cloud")
	}
	return d.personalObjFromUploadResp(resp, targetName, info.Size, info.SHA256), nil
}

func (d *Yun139) linkCASVideo(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	info, err := d.parseCASFromObj(ctx, file)
	if err != nil {
		return nil, err
	}
	previewName, err := resolveCASRestoreName(file.GetName(), info)
	if err != nil {
		return nil, err
	}
	if !casmeta.ExtAllowed(previewName, d.CASExtAllowlist) || (!d.CASDownloadRestore && !isVideoName(previewName)) {
		return d.Link(ctx, file, model.LinkArgs{IP: args.IP, Header: args.Header, Type: "raw_cas", Redirect: args.Redirect})
	}
	tempRoot, err := d.ensurePersonalTempDir(ctx)
	if err != nil {
		return nil, err
	}
	restoreInfo := *info
	restoreInfo.Name = previewName
	tempObj, err := d.restoreCAS(ctx, tempRoot, &restoreInfo, casmeta.FileName(previewName), true)
	if err != nil {
		return nil, err
	}
	url, err := d.personalGetLink(tempObj.GetID())
	if err != nil {
		_ = d.deletePersonalPermanently(context.TODO(), tempObj)
		return nil, err
	}
	go func() {
		if err := d.deletePersonalPermanently(context.TODO(), tempObj); err != nil {
			utils.Log.Errorf("cas139TempDeleteError:%s", err)
		}
	}()
	return newCASVideoLink(url, info.Size), nil
}

func newCASVideoLink(url string, size int64) *model.Link {
	return &model.Link{
		URL:              url,
		ContentLength:    size,
		RequireReference: true,
	}
}

func (d *Yun139) ensurePersonalTempDir(ctx context.Context) (model.Obj, error) {
	if obj, err := d.findPersonalFolderByName(ctx, casTempDirName, d.RootFolderID); err == nil {
		return obj, nil
	}
	data := base.Json{
		"parentFileId":   d.RootFolderID,
		"name":           casTempDirName,
		"description":    "",
		"type":           "folder",
		"fileRenameMode": "force_rename",
	}
	var resp PersonalUploadResp
	_, err := d.personalPost("/file/create", data, &resp)
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       resp.Data.FileId,
		Name:     casTempDirName,
		IsFolder: true,
		Modified: time.Now(),
		Ctime:    time.Now(),
	}, nil
}

func (d *Yun139) findPersonalFileByName(ctx context.Context, name string, parentId string) (model.Obj, error) {
	return d.findPersonalObjByName(ctx, name, parentId, false)
}

func (d *Yun139) findPersonalFolderByName(ctx context.Context, name string, parentId string) (model.Obj, error) {
	return d.findPersonalObjByName(ctx, name, parentId, true)
}

func (d *Yun139) findPersonalObjByName(ctx context.Context, name string, parentId string, folder bool) (model.Obj, error) {
	items, err := d.personalGetFiles(parentId)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.GetName() == name && item.IsDir() == folder {
			return item, nil
		}
	}
	return nil, fmt.Errorf("object not found: %s", name)
}

func (d *Yun139) deletePersonalPermanently(ctx context.Context, obj model.Obj) error {
	if obj == nil || obj.GetID() == "" {
		return nil
	}
	if err := d.Remove(ctx, obj); err != nil {
		return err
	}
	_, err := d.personalPost("/file/batchDelete", base.Json{"fileIds": []string{obj.GetID()}}, nil)
	return err
}

func headerToMap(header http.Header) map[string]string {
	headers := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return headers
}

func isVideoName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv", ".ts", ".m2ts", ".wmv", ".rmvb", ".m4v", ".mpg", ".mpeg", ".3gp":
		return true
	default:
		return false
	}
}

func randomSuffix() string {
	s := fmt.Sprintf("%d", time.Now().UnixNano())
	if len(s) > 5 {
		return s[len(s)-5:]
	}
	return s
}
