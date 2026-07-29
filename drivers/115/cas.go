package _115

import (
	"bytes"
	"context"
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
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/google/uuid"
)

const (
	casProvider115 = "115"
	casTempDirName = "TEMP"
)

type casUploadInfo = casmeta.Info

func isCASName(name string) bool {
	return casmeta.IsName(name)
}

func (d *Pan115) shouldUploadCAS(name string) bool {
	return d.GenerateCAS && !isCASName(name) && casmeta.ExtAllowed(name, d.CASExtAllowlist)
}

func (d *Pan115) shouldDeleteSource() bool {
	return d.GenerateCAS && d.DeleteSource
}

func (d *Pan115) CASDownloadRestoreEnabled() bool {
	return d.CASDownloadRestore
}

func (d *Pan115) shouldPlayCAS(file model.Obj, args model.LinkArgs) bool {
	if !isCASName(file.GetName()) {
		return false
	}
	return strings.EqualFold(args.Type, "cas_video")
}

func (d *Pan115) CASPreviewName(ctx context.Context, file model.Obj) (string, error) {
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

func (d *Pan115) prepareCASPut(ctx context.Context, dstDir model.Obj, file model.FileStreamer) (model.FileStreamer, model.Obj, bool, error) {
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

func (d *Pan115) uploadCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo) (model.Obj, error) {
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
	uploadedCASObj, _, err := d.putRaw(ctx, dstDir, casStream, func(float64) {})
	if err != nil {
		return nil, err
	}
	if uploadedCASObj != nil {
		return uploadedCASObj, nil
	}
	return casObj, nil
}

func (d *Pan115) deleteSource(ctx context.Context, uploadedObj model.Obj) error {
	if uploadedObj == nil || uploadedObj.GetID() == "" {
		return nil
	}
	return d.Remove(ctx, uploadedObj)
}

func (d *Pan115) parseCAS(data []byte) (*casUploadInfo, error) {
	return casmeta.Decode(data)
}

func (d *Pan115) parseCASFromObj(ctx context.Context, file model.Obj) (*casUploadInfo, error) {
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

func (d *Pan115) validateCASInfo(info *casUploadInfo) error {
	if info == nil {
		return fmt.Errorf("cas restore failed: missing cas payload")
	}
	if !strings.EqualFold(info.Provider, casProvider115) {
		return fmt.Errorf("cas restore failed: unsupported provider %q", info.Provider)
	}
	if info.SHA1 == "" {
		return fmt.Errorf("cas restore failed: missing sha1")
	}
	if info.PreID == "" {
		return fmt.Errorf("cas restore failed: missing preID")
	}
	return nil
}

func (d *Pan115) restoreCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo, casName string, temp bool) (model.Obj, error) {
	targetName, err := resolveCASRestoreName(casName, info)
	if err != nil {
		return nil, err
	}
	if !casmeta.ExtAllowed(targetName, d.CASExtAllowlist) {
		return nil, fmt.Errorf("cas restore skipped: extension of %q is not allowed", targetName)
	}
	if err = d.validateCASInfo(info); err != nil {
		return nil, err
	}
	if temp {
		targetName = fmt.Sprintf("TEMP_%d_%s_%s", time.Now().UnixNano()/1e6, uuid.NewString()[:5], targetName)
	}
	if existing, err := d.findFileByName(ctx, targetName, dstDir.GetID()); err == nil && !temp {
		return existing, nil
	}
	stream := &casRestoreStream{info: info, name: targetName}
	resp, err := d.rapidUpload(info.Size, targetName, dstDir.GetID(), strings.ToUpper(info.PreID), strings.ToUpper(info.SHA1), stream)
	if err != nil {
		return nil, err
	}
	matched, err := resp.Ok()
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, fmt.Errorf("cas restore failed: source file data does not exist in cloud")
	}
	file, err := d.getNewFileByPickCode(resp.PickCode)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (d *Pan115) linkCASVideo(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
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
	tempRoot, err := d.ensureTempDir(ctx)
	if err != nil {
		return nil, err
	}
	tempObj, err := d.restoreCAS(ctx, tempRoot, info, casmeta.FileName(previewName), true)
	if err != nil {
		return nil, err
	}
	link, err := d.Link(ctx, tempObj, model.LinkArgs{IP: args.IP, Header: args.Header, Type: "raw_file", Redirect: args.Redirect})
	if err != nil {
		_ = d.Remove(context.TODO(), tempObj)
		return nil, err
	}
	go func() {
		if err := d.Remove(context.TODO(), tempObj); err != nil {
			utils.Log.Errorf("cas115TempDeleteError:%s", err)
		}
	}()
	link.ContentLength = info.Size
	link.RequireReference = true
	return link, nil
}

func (d *Pan115) ensureTempDir(ctx context.Context) (model.Obj, error) {
	if obj, err := d.findFolderByName(ctx, casTempDirName, d.RootFolderID); err == nil {
		return obj, nil
	}
	root := d.RootFolderID
	if root == "" {
		root = "0"
	}
	return d.MakeDir(ctx, &model.Object{ID: root, Name: root, IsFolder: true}, casTempDirName)
}

func (d *Pan115) findFileByName(ctx context.Context, name, dirID string) (model.Obj, error) {
	files, err := d.getFiles(dirID)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if !files[i].IsDir() && files[i].GetName() == name {
			return &files[i], nil
		}
	}
	return nil, driver115.ErrNotExist
}

func (d *Pan115) findFolderByName(ctx context.Context, name, dirID string) (model.Obj, error) {
	files, err := d.getFiles(dirID)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if files[i].IsDir() && files[i].GetName() == name {
			return &files[i], nil
		}
	}
	return nil, driver115.ErrNotExist
}

type casRestoreStream struct {
	info *casUploadInfo
	name string
	utils.Closers
}

func (s *casRestoreStream) Read([]byte) (int, error) {
	return 0, fmt.Errorf("cas restore stream has no source bytes")
}

func (s *casRestoreStream) GetSize() int64 {
	return s.info.Size
}

func (s *casRestoreStream) GetName() string {
	return s.name
}

func (s *casRestoreStream) ModTime() time.Time {
	return time.Now()
}

func (s *casRestoreStream) CreateTime() time.Time {
	return s.ModTime()
}

func (s *casRestoreStream) IsDir() bool {
	return false
}

func (s *casRestoreStream) GetHash() utils.HashInfo {
	return utils.NewHashInfo(utils.SHA1, s.info.SHA1)
}

func (s *casRestoreStream) GetID() string {
	return ""
}

func (s *casRestoreStream) GetPath() string {
	return ""
}

func (s *casRestoreStream) GetMimetype() string {
	return utils.GetMimeType(s.name)
}

func (s *casRestoreStream) NeedStore() bool {
	return false
}

func (s *casRestoreStream) IsForceStreamUpload() bool {
	return false
}

func (s *casRestoreStream) GetExist() model.Obj {
	return nil
}

func (s *casRestoreStream) SetExist(model.Obj) {}

func (s *casRestoreStream) RangeRead(httpRange http_range.Range) (io.Reader, error) {
	return nil, fmt.Errorf("cas restore requires source bytes for range %d-%d", httpRange.Start, httpRange.Start+httpRange.Length-1)
}

func (s *casRestoreStream) CacheFullAndWriter(*model.UpdateProgress, io.Writer) (model.File, error) {
	return nil, fmt.Errorf("cas restore stream has no source bytes")
}

func (s *casRestoreStream) GetFile() model.File {
	return nil
}

func isVideoName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv", ".ts", ".m2ts", ".wmv", ".rmvb", ".m4v", ".mpg", ".mpeg", ".3gp":
		return true
	default:
		return false
	}
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
