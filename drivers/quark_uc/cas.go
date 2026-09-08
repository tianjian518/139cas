package quark

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
	"github.com/go-resty/resty/v2"
)

const (
	casProviderQuark = "quark"
	casTempDirName   = "TEMP"
)

type casUploadInfo = casmeta.Info

func isCASName(name string) bool {
	return casmeta.IsName(name)
}

func (d *QuarkOrUC) shouldUploadCAS(name string) bool {
	return d.GenerateCAS && !isCASName(name) && casmeta.ExtAllowed(name, d.CASExtAllowlist)
}

func (d *QuarkOrUC) shouldDeleteSource() bool {
	return d.GenerateCAS && d.DeleteSource
}

func (d *QuarkOrUC) CASDownloadRestoreEnabled() bool {
	return d.CASDownloadRestore
}

func (d *QuarkOrUC) shouldPlayCAS(file model.Obj, args model.LinkArgs) bool {
	if !isCASName(file.GetName()) {
		return false
	}
	return strings.EqualFold(args.Type, "cas_video")
}

func (d *QuarkOrUC) CASPreviewName(ctx context.Context, file model.Obj) (string, error) {
	if !isCASName(file.GetName()) {
		return file.GetName(), nil
	}
	info, err := d.parseCASFromObj(ctx, file)
	if err != nil {
		return "", err
	}
	previewName, err := casmeta.ResolveRestoreName(file.GetName(), info)
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

// prepareCASPut handles the case where the uploaded file is itself a .cas file:
// it restores the real file from the CAS metadata instead of storing the .cas.
func (d *QuarkOrUC) prepareCASPut(ctx context.Context, dstDir model.Obj, file model.FileStreamer) (model.FileStreamer, model.Obj, bool, error) {
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
	restoredName, err := casmeta.ResolveRestoreName(file.GetName(), info)
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

func (d *QuarkOrUC) uploadCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo) (model.Obj, error) {
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
	uploadedCASObj, _, _, err := d.uploadFile(ctx, dstDir, casStream, func(float64) {})
	if err != nil {
		return nil, err
	}
	if uploadedCASObj != nil {
		return uploadedCASObj, nil
	}
	return casObj, nil
}

func (d *QuarkOrUC) deleteSource(ctx context.Context, dstDir model.Obj, uploadedObj model.Obj, info *casUploadInfo) error {
	if info == nil || !d.shouldDeleteSource() || !d.shouldUploadCAS(info.Name) {
		return nil
	}
	if uploadedObj == nil || uploadedObj.GetID() == "" {
		var err error
		uploadedObj, err = d.findFileByName(ctx, info.Name, dstDir.GetID())
		if err != nil {
			return err
		}
	}
	return d.deletePermanently(ctx, uploadedObj)
}

// TransferCASEnabled 判断"夸克作为源端搬出"之后，是否要把夸克侧的原文件转成 .cas。
//
// 与 shouldUploadCAS 的区别：shouldUploadCAS 管的是"上传到夸克"这条链路，
// 这里管的是"从夸克下载/搬出到别的网盘"这条链路。两者共用同一个扩展名白名单。
func (d *QuarkOrUC) TransferCASEnabled(name string) bool {
	return d.TransferCAS && !isCASName(name) && casmeta.ExtAllowed(name, d.CASExtAllowlist)
}

// SaveTransferCAS 用搬运途中顺路算出的 md5+sha1，在夸克原目录生成 .cas 占位文件，
// 然后把夸克侧的原文件删掉。
//
// 调用方必须保证：目标存储已经上传成功。这里只负责夸克这一侧的收尾。
// 设计原则：.cas 没真正落盘之前绝不删原文件；任何一步失败都直接返回错误。
func (d *QuarkOrUC) SaveTransferCAS(ctx context.Context, dir model.Obj, obj model.Obj, md5Str, sha1Str string) error {
	info := &casUploadInfo{
		Provider: casProviderQuark,
		Name:     obj.GetName(),
		Size:     obj.GetSize(),
		MD5:      md5Str,
		SHA1:     sha1Str,
	}
	// 缺 md5 或 sha1 的 .cas 是废文件，还原必失败，直接拒绝
	if err := d.validateCASInfo(info); err != nil {
		return err
	}
	if _, err := d.uploadCAS(ctx, dir, info); err != nil {
		return err
	}
	if !d.shouldDeleteSource() {
		return nil
	}
	target := obj
	if target.GetID() == "" {
		var err error
		if target, err = d.findFileByName(ctx, obj.GetName(), dir.GetID()); err != nil {
			return err
		}
	}
	return d.deletePermanently(ctx, target)
}

// deletePermanently 尝试从网盘彻底删除文件（不进回收站）。
// 夸克的 /file/delete 用 action_type 区分：1 为移入回收站，2 为彻底删除。
// 彻底删除属非官方用法，失败时回退到普通删除，保证不会比原来更糟。
func (d *QuarkOrUC) deletePermanently(ctx context.Context, obj model.Obj) error {
	if !d.CASDeletePermanently {
		return d.Remove(ctx, obj)
	}
	if err := d.tryDeletePermanently(obj); err != nil {
		utils.Log.Warnf("[quark] 彻底删除 %q 失败，回退为回收站删除: %v", obj.GetName(), err)
		return d.Remove(ctx, obj)
	}
	return nil
}

// deleteTemp 清理播放时产生的临时文件。这类文件用户本就不需要保留，
// 因此总是优先尝试彻底删除，避免每次播放都往回收站里塞一份大文件。
func (d *QuarkOrUC) deleteTemp(ctx context.Context, obj model.Obj) error {
	if err := d.tryDeletePermanently(obj); err == nil {
		return nil
	}
	return d.Remove(ctx, obj)
}

func (d *QuarkOrUC) tryDeletePermanently(obj model.Obj) error {
	if obj == nil || obj.GetID() == "" {
		return nil
	}
	data := base.Json{
		"action_type":  2,
		"exclude_fids": []string{},
		"filelist":     []string{obj.GetID()},
	}
	_, err := d.request("/file/delete", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *QuarkOrUC) parseCAS(data []byte) (*casUploadInfo, error) {
	return casmeta.Decode(data)
}

func (d *QuarkOrUC) parseCASFromObj(ctx context.Context, file model.Obj) (*casUploadInfo, error) {
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

func (d *QuarkOrUC) validateCASInfo(info *casUploadInfo) error {
	if info == nil {
		return fmt.Errorf("cas restore failed: missing cas payload")
	}
	if info.Provider != "" && !strings.EqualFold(info.Provider, casProviderQuark) {
		return fmt.Errorf("cas restore failed: unsupported provider %q", info.Provider)
	}
	if info.MD5 == "" {
		return fmt.Errorf("cas restore failed: missing md5")
	}
	if info.SHA1 == "" {
		return fmt.Errorf("cas restore failed: missing sha1")
	}
	return nil
}

// restoreCAS re-creates the real file from CAS metadata via Quark's rapid-upload
// (md5 + sha1) API. No file bytes are transferred.
func (d *QuarkOrUC) restoreCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo, casName string, temp bool) (model.Obj, error) {
	targetName, err := casmeta.ResolveRestoreName(casName, info)
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
		targetName = fmt.Sprintf("TEMP_%d_%s_%s", time.Now().UnixNano()/1e6, randomSuffix(), targetName)
	}
	if !temp {
		if existing, err := d.findFileByName(ctx, targetName, dstDir.GetID()); err == nil {
			return existing, nil
		}
	}
	restoreInfo := *info
	restoreInfo.Name = targetName
	restoreStream := &casRestoreStream{info: &restoreInfo, name: targetName}

	pre, err := d.upPre(restoreStream, dstDir.GetID())
	if err != nil {
		return nil, err
	}
	if pre.Data.Finish && pre.Data.Fid != "" {
		return newFileObj(pre.Data.Fid, targetName, restoreInfo.Size), nil
	}
	hashResp, err := d.upHashResp(info.MD5, info.SHA1, pre.Data.TaskId)
	if err != nil {
		return nil, err
	}
	if !hashResp.Data.Finish {
		return nil, fmt.Errorf("cas restore failed: source file data does not exist in cloud")
	}
	if hashResp.Data.Fid == "" {
		return d.findFileByName(ctx, targetName, dstDir.GetID())
	}
	return newFileObj(hashResp.Data.Fid, targetName, restoreInfo.Size), nil
}

func (d *QuarkOrUC) linkCASVideo(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	info, err := d.parseCASFromObj(ctx, file)
	if err != nil {
		return nil, err
	}
	previewName, err := casmeta.ResolveRestoreName(file.GetName(), info)
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
	restoreInfo := *info
	restoreInfo.Name = previewName
	tempObj, err := d.restoreCAS(ctx, tempRoot, &restoreInfo, casmeta.FileName(previewName), true)
	if err != nil {
		return nil, err
	}
	// With UsePlayDirectLink enabled, prefer the cookie-free /file/v2/play URL
	// so OpenList can 302-redirect instead of proxying; fall back on failure.
	var link *model.Link
	if d.UsePlayDirectLink {
		link, err = d.getPlayLink(ctx, tempObj)
		if err != nil || link == nil {
			utils.Log.Warnf("casQuarkPlayLinkFallback:%v", err)
		}
	}
	if link == nil {
		link, err = d.getDownloadLink(tempObj)
		if err != nil {
			_ = d.deleteTemp(context.TODO(), tempObj)
			return nil, err
		}
	}
	go func() {
		if err := d.deleteTemp(context.TODO(), tempObj); err != nil {
			utils.Log.Errorf("casQuarkTempDeleteError:%s", err)
		}
	}()
	link.ContentLength = info.Size
	link.RequireReference = true
	return link, nil
}

func (d *QuarkOrUC) ensureTempDir(ctx context.Context) (model.Obj, error) {
	if obj, err := d.findFolderByName(ctx, casTempDirName, d.RootFolderID); err == nil {
		return obj, nil
	}
	root := d.RootFolderID
	if root == "" {
		root = "0"
	}
	if err := d.MakeDir(ctx, &model.Object{ID: root, Name: root, IsFolder: true}, casTempDirName); err != nil {
		return nil, err
	}
	return d.findFolderByName(ctx, casTempDirName, d.RootFolderID)
}

func (d *QuarkOrUC) findFileByName(ctx context.Context, name string, parentId string) (model.Obj, error) {
	return d.findObjByName(name, parentId, false)
}

func (d *QuarkOrUC) findFolderByName(ctx context.Context, name string, parentId string) (model.Obj, error) {
	return d.findObjByName(name, parentId, true)
}

func (d *QuarkOrUC) findObjByName(name string, parentId string, folder bool) (model.Obj, error) {
	files, err := d.GetFiles(parentId)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.GetName() == name && f.IsDir() == folder {
			return f, nil
		}
	}
	return nil, fmt.Errorf("object not found: %s", name)
}

func newFileObj(fid, name string, size int64) model.Obj {
	now := time.Now()
	return &File{
		Fid:        fid,
		FileName:   name,
		Size:       size,
		File:       true,
		CreatedAt:  now.UnixMilli(),
		UpdatedAt:  now.UnixMilli(),
		LCreatedAt: now.UnixMilli(),
		LUpdatedAt: now.UnixMilli(),
	}
}

// casRestoreStream is a metadata-only stream: it carries the name/size/hash of
// the file to restore but yields no bytes. Quark's rapid-upload flow only needs
// those values for the /file/upload/pre and /file/update/hash calls.
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
	return utils.NewHashInfoByMap(map[*utils.HashType]string{
		utils.MD5:  s.info.MD5,
		utils.SHA1: s.info.SHA1,
	})
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

func randomSuffix() string {
	s := fmt.Sprintf("%d", time.Now().UnixNano())
	if len(s) > 5 {
		return s[len(s)-5:]
	}
	return s
}
