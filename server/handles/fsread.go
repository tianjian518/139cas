package handles

import (
	"context"
	"fmt"
	stdpath "path"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type ListReq struct {
	model.PageReq
	Path     string `json:"path" form:"path"`
	Password string `json:"password" form:"password"`
	Refresh  bool   `json:"refresh"`
}

type DirReq struct {
	Path      string `json:"path" form:"path"`
	Password  string `json:"password" form:"password"`
	ForceRoot bool   `json:"force_root" form:"force_root"`
}

type ObjResp struct {
	Name         string                     `json:"name"`
	Size         int64                      `json:"size"`
	IsDir        bool                       `json:"is_dir"`
	Modified     time.Time                  `json:"modified"`
	Created      time.Time                  `json:"created"`
	Sign         string                     `json:"sign"`
	Thumb        string                     `json:"thumb"`
	Type         int                        `json:"type"`
	HashInfoStr  string                     `json:"hashinfo"`
	HashInfo     map[*utils.HashType]string `json:"hash_info"`
	MountDetails *model.StorageDetails      `json:"mount_details,omitempty"`
	// CASSource 为 true 时表示本项是 .cas 秒传文件，Size 为原始文件大小
	CASSource     bool   `json:"cas_source,omitempty"`
	CASSourceName string `json:"cas_source_name,omitempty"`
}

type FsListResp struct {
	Content            []ObjResp `json:"content"`
	Total              int64     `json:"total"`
	Readme             string    `json:"readme"`
	Header             string    `json:"header"`
	Write              bool      `json:"write"`
	WriteContentBypass bool      `json:"write_content_bypass"`
	Provider           string    `json:"provider"`
	DirectUploadTools  []string  `json:"direct_upload_tools,omitempty"`
}

func FsListSplit(c *gin.Context) {
	var req ListReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	req.Validate()
	if strings.HasPrefix(req.Path, "/@s") {
		req.Path = strings.TrimPrefix(req.Path, "/@s")
		SharingList(c, &req)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user.IsGuest() && user.Disabled {
		common.ErrorStrResp(c, "Guest user is disabled, login please", 401)
		return
	}
	FsList(c, &req, user)
}

func FsList(c *gin.Context, req *ListReq, user *model.User) {
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.GinAppendValues(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	canWriteContentAtPath := common.CanWrite(user, meta, reqPath) && (user.CanWriteContent() || common.CanWriteContentBypassUserPerms(meta, reqPath))
	if req.Refresh && !canWriteContentAtPath {
		common.ErrorStrResp(c, "Refresh without permission", 403)
		return
	}
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{
		Refresh:            req.Refresh,
		WithStorageDetails: !user.IsGuest() && !setting.GetBool(conf.HideStorageDetails),
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	total, objs := pagination(objs, &req.PageReq)
	provider := "unknown"
	var directUploadTools []string
	var storage driver.Driver
	if st, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{}); err == nil {
		storage = st
		if canWriteContentAtPath {
			directUploadTools = op.GetDirectUploadTools(st)
		}
	}
	common.SuccessResp(c, FsListResp{
		Content:            toObjsRespWithCAS(c.Request.Context(), objs, reqPath, isEncrypt(meta, reqPath), storage),
		Total:              int64(total),
		Readme:             getReadme(meta, reqPath),
		Header:             getHeader(meta, reqPath),
		Write:              common.CanWrite(user, meta, reqPath),
		WriteContentBypass: common.CanWriteContentBypassUserPerms(meta, reqPath),
		Provider:           provider,
		DirectUploadTools:  directUploadTools,
	})
}

func FsDirs(c *gin.Context) {
	var req DirReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	reqPath := req.Path
	if req.ForceRoot {
		if !user.IsAdmin() {
			common.ErrorStrResp(c, "Permission denied", 403)
			return
		}
	} else {
		tmp, err := user.JoinPath(req.Path)
		if err != nil {
			common.ErrorResp(c, err, 403)
			return
		}
		reqPath = tmp
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.GinAppendValues(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	dirs := filterDirs(objs)
	common.SuccessResp(c, dirs)
}

type DirResp struct {
	Name     string    `json:"name"`
	Modified time.Time `json:"modified"`
}

func filterDirs(objs []model.Obj) []DirResp {
	var dirs []DirResp
	for _, obj := range objs {
		if obj.IsDir() {
			dirs = append(dirs, DirResp{
				Name:     obj.GetName(),
				Modified: obj.ModTime(),
			})
		}
	}
	return dirs
}

func getReadme(meta *model.Meta, path string) string {
	if meta != nil && common.MetaCoversPath(meta.Path, path, meta.RSub) {
		return meta.Readme
	}
	return ""
}

func getHeader(meta *model.Meta, path string) string {
	if meta != nil && common.MetaCoversPath(meta.Path, path, meta.HeaderSub) {
		return meta.Header
	}
	return ""
}

func isEncrypt(meta *model.Meta, path string) bool {
	if common.IsStorageSignEnabled(path) {
		return true
	}
	if meta == nil || meta.Password == "" {
		return false
	}
	if !common.MetaCoversPath(meta.Path, path, meta.PSub) {
		return false
	}
	return true
}

func pagination(objs []model.Obj, req *model.PageReq) (int, []model.Obj) {
	pageIndex, pageSize := req.Page, req.PerPage
	total := len(objs)
	start := (pageIndex - 1) * pageSize
	if start > total {
		return total, []model.Obj{}
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return total, objs[start:end]
}

func toObjsResp(objs []model.Obj, parent string, encrypt bool) []ObjResp {
	return toObjsRespWithCAS(context.Background(), objs, parent, encrypt, nil)
}

// toObjsRespWithCAS 与 toObjsResp 一致，但对 .cas 秒传文件额外解析其原始文件名与大小。
// 解析需要读取 .cas 内容（数 KB），因此并发执行并限制并发度。
func toObjsRespWithCAS(ctx context.Context, objs []model.Obj, parent string, encrypt bool, storage driver.Driver) []ObjResp {
	resp := make([]ObjResp, len(objs))
	sizer, _ := storage.(driver.CASSourceSizer)
	needsCAS := make([]int, 0, 8)
	for i, obj := range objs {
		thumb, _ := model.GetThumb(obj)
		mountDetails, _ := model.GetStorageDetails(obj)
		resp[i] = ObjResp{
			Name:         obj.GetName(),
			Size:         obj.GetSize(),
			IsDir:        obj.IsDir(),
			Modified:     obj.ModTime(),
			Created:      obj.CreateTime(),
			HashInfoStr:  obj.GetHash().String(),
			HashInfo:     obj.GetHash().Export(),
			Sign:         common.Sign(obj, parent, encrypt),
			Thumb:        thumb,
			Type:         utils.GetObjType(obj.GetName(), obj.IsDir()),
			MountDetails: mountDetails,
		}
		if sizer != nil && !obj.IsDir() && isCASFileName(obj.GetName()) {
			needsCAS = append(needsCAS, i)
		}
	}
	if len(needsCAS) == 0 {
		return resp
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, idx := range needsCAS {
		obj := objs[idx]
		wg.Add(1)
		go func(i int, o model.Obj) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			size, name, err := sizer.CASSourceSize(ctx, o)
			if err != nil {
				return
			}
			resp[i].Size = size
			resp[i].CASSource = true
			resp[i].CASSourceName = name
		}(idx, obj)
	}
	wg.Wait()
	return resp
}

func isCASFileName(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".cas")
}

type FsGetReq struct {
	Path     string `json:"path" form:"path"`
	Password string `json:"password" form:"password"`
}

type FsGetResp struct {
	ObjResp
	RawURL   string    `json:"raw_url"`
	Readme   string    `json:"readme"`
	Header   string    `json:"header"`
	Provider string    `json:"provider"`
	Related  []ObjResp `json:"related"`
}

func FsGetSplit(c *gin.Context) {
	var req FsGetReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if strings.HasPrefix(req.Path, "/@s") {
		req.Path = strings.TrimPrefix(req.Path, "/@s")
		SharingGet(c, &req)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user.IsGuest() && user.Disabled {
		common.ErrorStrResp(c, "Guest user is disabled, login please", 401)
		return
	}
	FsGet(c, &req, user)
}

func FsGet(c *gin.Context, req *FsGetReq, user *model.User) {
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.GinAppendValues(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	obj, err := fs.Get(c.Request.Context(), reqPath, &fs.GetArgs{
		WithStorageDetails: !user.IsGuest() && !setting.GetBool(conf.HideStorageDetails),
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	var rawURL string

	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	provider, ok := model.GetProvider(obj)
	if !ok && err == nil {
		provider = storage.Config().Name
	}
	typeName := obj.GetName()
	if err == nil {
		typeName = resolveCASPreviewTypeName(c.Request.Context(), storage, obj)
	}
	if !obj.IsDir() {
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		if storage.Config().MustProxy() || storage.GetStorage().WebProxy {
			rawURL = common.GenerateDownProxyURL(storage.GetStorage(), reqPath)
			if rawURL == "" {
				query := ""
				if isEncrypt(meta, reqPath) || setting.GetBool(conf.SignAll) {
					query = "?sign=" + sign.Sign(reqPath)
				}
				rawURL = fmt.Sprintf("%s/p%s%s",
					common.GetApiUrl(c),
					utils.EncodePath(reqPath, true),
					query)
			}
		} else if typeName != obj.GetName() {
			rawURL = buildObjectAccessURL(c, reqPath, isEncrypt(meta, reqPath), common.ShouldProxy(storage, typeName))
		} else {
			// file have raw url
			if url, ok := model.GetUrl(obj); ok {
				rawURL = url
			} else {
				// if storage is not proxy, use raw url by fs.Link
				link, _, err := fs.Link(c.Request.Context(), reqPath, model.LinkArgs{
					IP:       c.ClientIP(),
					Header:   c.Request.Header,
					Redirect: true,
				})
				if err != nil {
					common.ErrorResp(c, err, 500)
					return
				}
				defer link.Close()
				rawURL = link.URL
			}
		}
	}
	var related []model.Obj
	parentPath := stdpath.Dir(reqPath)
	sameLevelFiles, err := fs.List(c.Request.Context(), parentPath, &fs.ListArgs{})
	if err == nil {
		related = filterRelated(sameLevelFiles, obj)
	}
	parentMeta, _ := op.GetNearestMeta(parentPath)
	thumb, _ := model.GetThumb(obj)
	mountDetails, _ := model.GetStorageDetails(obj)
	common.SuccessResp(c, FsGetResp{
		ObjResp: ObjResp{
			Name:         obj.GetName(),
			Size:         obj.GetSize(),
			IsDir:        obj.IsDir(),
			Modified:     obj.ModTime(),
			Created:      obj.CreateTime(),
			HashInfoStr:  obj.GetHash().String(),
			HashInfo:     obj.GetHash().Export(),
			Sign:         common.Sign(obj, parentPath, isEncrypt(meta, reqPath)),
			Type:         utils.GetFileType(typeName),
			Thumb:        thumb,
			MountDetails: mountDetails,
		},
		RawURL:   rawURL,
		Readme:   getReadme(meta, reqPath),
		Header:   getHeader(meta, reqPath),
		Provider: provider,
		Related:  toObjsResp(related, parentPath, isEncrypt(parentMeta, parentPath)),
	})
}

func resolveCASPreviewTypeName(ctx context.Context, storage driver.Driver, obj model.Obj) string {
	if obj == nil {
		return ""
	}
	namer, ok := storage.(driver.CASPreviewNamer)
	if !ok || obj.IsDir() {
		return obj.GetName()
	}
	name, err := namer.CASPreviewName(ctx, obj)
	if err != nil || name == "" {
		return obj.GetName()
	}
	return name
}

func buildObjectAccessURL(c *gin.Context, reqPath string, encrypt bool, proxy bool) string {
	query := ""
	if encrypt || setting.GetBool(conf.SignAll) {
		query = "?sign=" + sign.Sign(reqPath)
	}
	prefix := "/d"
	if proxy {
		prefix = "/p"
	}
	return fmt.Sprintf("%s%s%s%s", common.GetApiUrl(c), prefix, utils.EncodePath(reqPath, true), query)
}

func filterRelated(objs []model.Obj, obj model.Obj) []model.Obj {
	var related []model.Obj
	nameWithoutExt := strings.TrimSuffix(obj.GetName(), stdpath.Ext(obj.GetName()))
	for _, o := range objs {
		if o.GetName() == obj.GetName() {
			continue
		}
		if strings.HasPrefix(o.GetName(), nameWithoutExt) {
			related = append(related, o)
		}
	}
	return related
}

type FsOtherReq struct {
	model.FsOtherArgs
	Password string `json:"password" form:"password"`
}

func FsOther(c *gin.Context) {
	var req FsOtherReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	var err error
	req.Path, err = user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(req.Path)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500)
		return
	}
	common.GinAppendValues(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, req.Path, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	res, err := fs.Other(c.Request.Context(), req.FsOtherArgs)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, res)
}
