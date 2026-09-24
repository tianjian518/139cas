package guangyapan

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

const defaultClientID = "aMe-8VSlkrbQXpUR"

// tokenRefreshAhead 是 access_token 到期前提前主动续期的时间窗，
// 避免令牌过期瞬间所有并发请求同时收到 401 而一起触发刷新。
const tokenRefreshAhead = 5 * time.Minute

// errTokenInvalid 表示 refresh_token 已被服务端作废（一次性轮换后被并发使用）。
var errTokenInvalid = errors.New("光鸭 refresh_token 已失效（令牌一次性轮换，可能被并发刷新作废），请重新用手机号+短信验证码登录")

// refreshKeyLocks 按 refresh_token 前缀做进程级串行。
// OpenList 在编辑/重载存储时会短暂同时存在新旧两个 driver 实例，
// 实例锁挡不住这种情况，故再按令牌加一道进程级锁。
var refreshKeyLocks sync.Map

func refreshKeyLock(refreshToken string) *sync.Mutex {
	key := strings.TrimSpace(refreshToken)
	if len(key) > 24 {
		key = key[:24]
	}
	mu, _ := refreshKeyLocks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// jwtExpiry 解析 access_token 里的 exp（不校验签名，仅用于判断何时该续期）。
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claim struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claim); err != nil || claim.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claim.Exp, 0)
}

var (
	accountBaseURL = "https://account.guangyapan.com"
	apiBaseURL     = "https://api.guangyapan.com"
)

func (d *GuangYaPan) initClient() {
	if d.ClientID == "" {
		d.ClientID = defaultClientID
	}
	if d.DeviceID = normalizeDeviceID(d.DeviceID); d.DeviceID == "" {
		d.DeviceID = randomDeviceID()
	}
	deviceSign := strings.TrimSpace(d.DeviceSign)
	if deviceSign == "" {
		deviceSign = "wdi10." + d.DeviceID
	}
	if d.PageSize <= 0 {
		d.PageSize = 100
	}
	if d.OrderBy == 0 {
		d.OrderBy = 3
	}
	if d.SortType == 0 {
		d.SortType = 1
	}

	d.client = newHTTPClient()
	d.client.SetHeaders(commonHeaders(d.ClientID, d.DeviceID, deviceSign))

	// 账号域独立一个 client（登录 / 刷新令牌 / 校验令牌用），与文件 API 域分开。
	d.accountClient = newHTTPClient()
	d.accountClient.SetHeaders(accountHeaders(d.ClientID, d.DeviceID, deviceSign))
	if accountBaseURL != "" {
		d.accountClient.SetBaseURL(accountBaseURL)
	}
	if apiBaseURL != "" {
		d.client.SetBaseURL(apiBaseURL)
	}
}

// newHTTPClient 构造 HTTP 客户端。
// 说明：函数体刻意只调用 resty.New()，不使用 base.NewRestyClient()——后者会读取全局
// conf.Conf，在配置未初始化时（例如单元测试）会 panic。
func newHTTPClient() *resty.Client {
	return resty.New()
}

func commonHeaders(clientID, deviceID, deviceSign string) map[string]string {
	return map[string]string{
		"Accept":             "application/json, text/plain, */*",
		"Content-Type":       "application/json",
		"Referer":            "https://www.guangyupan.com/",
		"User-Agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36",
		"Accept-Language":    "zh-CN",
		"X-Client-Id":        clientID,
		"X-Client-Version":   "0.0.1",
		"X-Device-Id":        deviceID,
		"X-Device-Model":     "chrome%2F147.0.0.0",
		"X-Device-Name":      "PC-Chrome",
		"X-Device-Sign":      deviceSign,
		"X-Net-Work-Type":    "NONE",
		"X-Os-Version":       "Win32",
		"X-Platform-Version": "1",
		"X-Protocol-Version": "301",
		"X-Provider-Name":    "NONE",
		"X-Sdk-Version":      "9.0.2",
	}
}

// accountHeaders 是账号域（account.guangyapan.com）所需请求头。
func accountHeaders(clientID, deviceID, deviceSign string) map[string]string {
	return map[string]string{
		"Accept":             "application/json, text/plain, */*",
		"Content-Type":       "application/json",
		"X-Device-Model":     "chrome%2F147.0.0.0",
		"X-Device-Name":      "PC-Chrome",
		"X-Device-Sign":      deviceSign,
		"X-Net-Work-Type":    "NONE",
		"X-OS-Version":       "MacIntel",
		"X-Platform-Version": "1",
		"X-Protocol-Version": "301",
		"X-Provider-Name":    "NONE",
		"X-SDK-Version":      "9.0.2",
		"X-Client-Id":        clientID,
		"X-Client-Version":   "0.0.1",
		"X-Device-Id":        deviceID,
	}
}

func (d *GuangYaPan) request(ctx context.Context, method, url string, body any, result any, retryOnUnauthorized bool) error {
	// 主动续期：令牌快到期就先刷新，避免过期瞬间一堆并发请求同时 401。
	if d.tokenNearExpiry() && strings.TrimSpace(d.RefreshToken) != "" {
		if err := d.refreshAccessTokenAfter(ctx, atomic.LoadUint64(&d.tokenVersion)); err != nil {
			log.Warnf("guangyapan: 主动续期失败: %v", err)
		}
	}
	req := d.client.R().
		SetContext(ctx).
		SetHeaders(d.authHeaders())
	if body != nil {
		req.SetBody(body)
	}
	if result != nil {
		req.SetResult(result)
	}

	var resp *resty.Response
	var err error
	switch method {
	case http.MethodGet:
		resp, err = req.Get(url)
	case http.MethodPost:
		resp, err = req.Post(url)
	default:
		return fmt.Errorf("unsupported method %s", method)
	}
	if err != nil {
		return err
	}
	if resp.StatusCode() == http.StatusUnauthorized && retryOnUnauthorized && d.RefreshToken != "" {
		// 记下当前令牌版本，锁内若发现已被别的协程刷新则直接复用新令牌，避免重复刷新把令牌轮换作废。
		gen := atomic.LoadUint64(&d.tokenVersion)
		if err := d.refreshAccessTokenAfter(ctx, gen); err != nil {
			return err
		}
		return d.request(ctx, method, url, body, result, false)
	}
	if resp.IsError() {
		return fmt.Errorf("http %d: %s", resp.StatusCode(), strings.TrimSpace(string(resp.Body())))
	}
	return nil
}

func (d *GuangYaPan) authHeaders() map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + d.AccessToken,
		"Did":           d.DeviceID,
		"Dt":            "4",
		"did":           d.DeviceID,
		"dt":            "4",
	}
	if d.AccessToken != "" {
		headers["accessToken"] = d.AccessToken
	}
	return headers
}

// refreshAccessToken 刷新令牌（带并发保护）。
func (d *GuangYaPan) refreshAccessToken(ctx context.Context) error {
	return d.refreshAccessTokenAfter(ctx, atomic.LoadUint64(&d.tokenVersion))
}

// refreshAccessTokenAfter 只在令牌版本仍是 gen 时才真正刷新；
// 若期间已被其它协程刷新成功，则直接复用新令牌，绝不重复刷新。
func (d *GuangYaPan) refreshAccessTokenAfter(ctx context.Context, gen uint64) error {
	if d.accountClient == nil {
		d.initClient()
	}
	if strings.TrimSpace(d.RefreshToken) == "" {
		return errors.New("refresh_token is empty")
	}

	// 进程级串行：同一 refresh_token 同一时刻只允许一个刷新请求。
	keyMu := refreshKeyLock(d.RefreshToken)
	keyMu.Lock()
	defer keyMu.Unlock()

	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()

	if d.tokenDead {
		return errTokenInvalid
	}
	// 双检：等待锁期间已被其它协程刷新过，直接复用。
	if d.tokenVersion != gen {
		return nil
	}

	var resp tokenResp
	httpResp, err := d.accountRequest().
		SetContext(ctx).
		SetBody(map[string]any{
			"grant_type":    "refresh_token",
			"refresh_token": strings.TrimSpace(d.RefreshToken),
			"client_id":     d.ClientID,
		}).
		SetResult(&resp).
		Post("/v1/auth/token")
	if err != nil {
		return err
	}
	if httpResp.IsError() || strings.TrimSpace(resp.AccessToken) == "" {
		// 4126 / invalid_grant：refresh_token 一次性轮换已被作废，只能重新短信登录。
		body := httpResp.String()
		if resp.Error == "invalid_grant" || resp.ErrorCode == 4126 ||
			strings.Contains(body, "invalid refresh token") || strings.Contains(body, "invalidate refresh token") {
			d.tokenDead = true
			log.Errorf("guangyapan: refresh_token 已失效: %s", d.accountErr(resp.ErrorDesc, resp.Error, body))
			return errTokenInvalid
		}
		return fmt.Errorf("refresh token failed: %s", d.accountErr(resp.ErrorDesc, resp.Error, body))
	}

	d.AccessToken = strings.TrimSpace(resp.AccessToken)
	if strings.TrimSpace(resp.RefreshToken) != "" {
		d.RefreshToken = strings.TrimSpace(resp.RefreshToken)
	}
	d.tokenVersion++
	d.tokenDead = false
	d.tokenExpiry = tokenExpiryFrom(resp.AccessToken, resp.ExpiresIn)
	// 刷新成功后立刻把新令牌落库，避免重启后又要用已作废的旧值。
	persistAddition(d)
	d.client.SetHeaders(commonHeaders(d.ClientID, d.DeviceID, d.deviceSignOrDefault()))
	return nil
}

// tokenExpiryFrom 由 JWT exp 或 expires_in 推算过期时间。
func tokenExpiryFrom(accessToken string, expiresIn int64) time.Time {
	if exp := jwtExpiry(accessToken); !exp.IsZero() {
		return exp
	}
	if expiresIn > 0 {
		return time.Now().Add(time.Duration(expiresIn) * time.Second)
	}
	return time.Time{}
}

// tokenNearExpiry 判断 access_token 是否即将过期（未解析到过期时间则不主动续期）。
func (d *GuangYaPan) tokenNearExpiry() bool {
	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()
	if d.tokenExpiry.IsZero() {
		return false
	}
	return time.Now().Add(tokenRefreshAhead).After(d.tokenExpiry)
}

func (d *GuangYaPan) deviceSignOrDefault() string {
	if sign := strings.TrimSpace(d.DeviceSign); sign != "" {
		return sign
	}
	return "wdi10." + d.DeviceID
}

func (d *GuangYaPan) listAll(ctx context.Context, parentID string) ([]model.Obj, error) {
	pageSize := d.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	var objs []model.Obj
	for page := 0; ; page++ {
		var resp apiResponse
		err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/file/get_file_list", map[string]any{
			"parentId":  parentID,
			"page":      page,
			"pageSize":  pageSize,
			"orderBy":   d.OrderBy,
			"sortType":  d.SortType,
			"fileTypes": []any{},
		}, &resp, true)
		if err != nil {
			return nil, err
		}
		if !isSuccess(resp) {
			return nil, responseError(resp)
		}
		items := extractList(resp)
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			objs = append(objs, rawFileToObj(item))
		}
		total := intFromAny(findValue(dataMap(resp), "total"))
		if len(items) < pageSize || total > 0 && len(objs) >= total {
			break
		}
	}
	return objs, nil
}

func (d *GuangYaPan) downloadURL(ctx context.Context, file model.Obj) (string, error) {
	var resp apiResponse
	err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/get_res_download_url", map[string]any{
		"fileId": file.GetID(),
	}, &resp, true)
	if err != nil {
		return "", err
	}
	if isSuccess(resp) {
		if url := firstString(dataMap(resp), "signedURL", "downloadUrl", "download_url", "url"); url != "" {
			return url, nil
		}
	}
	if intFromAny(resp["code"]) != 143 {
		return "", responseError(resp)
	}

	driveID := ""
	if obj, ok := file.(*Object); ok {
		driveID = obj.DriveID
	}
	if driveID == "" {
		return "", errors.New("normal download failed and gcid is empty")
	}
	var vodResp apiResponse
	err = d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/file/get_vod_download_url", map[string]any{
		"fileId": file.GetID(),
		"gcid":   driveID,
	}, &vodResp, true)
	if err != nil {
		return "", err
	}
	if !isSuccess(vodResp) {
		return "", responseError(vodResp)
	}
	if url := firstString(dataMap(vodResp), "signedURL", "downloadUrl", "download_url", "url"); url != "" {
		return url, nil
	}
	return "", errors.New("download url not found")
}

func (d *GuangYaPan) createDir(ctx context.Context, parentID, dirName string) error {
	var resp apiResponse
	err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/file/create_dir", map[string]any{
		"parentId":        parentID,
		"dirName":         dirName,
		"failIfNameExist": true,
	}, &resp, true)
	if err != nil {
		return err
	}
	if !isSuccess(resp) {
		return responseError(resp)
	}
	return nil
}

func (d *GuangYaPan) moveFile(ctx context.Context, fileID, parentID string) error {
	return d.transferFile(ctx, "move_file", fileID, parentID)
}

func (d *GuangYaPan) copyFile(ctx context.Context, fileID, parentID string) error {
	return d.transferFile(ctx, "copy_file", fileID, parentID)
}

func (d *GuangYaPan) transferFile(ctx context.Context, action, fileID, parentID string) error {
	var resp apiResponse
	err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/file/"+action, map[string]any{
		"fileIds":  []string{fileID},
		"parentId": parentID,
	}, &resp, true)
	if err != nil {
		return err
	}
	if !isSuccess(resp) {
		return responseError(resp)
	}
	return nil
}

func (d *GuangYaPan) renameFile(ctx context.Context, fileID, newName string) error {
	var resp apiResponse
	err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/file/rename", map[string]any{
		"fileId":  fileID,
		"newName": newName,
	}, &resp, true)
	if err != nil {
		return err
	}
	if !isSuccess(resp) {
		return responseError(resp)
	}
	return nil
}

func (d *GuangYaPan) deleteFile(ctx context.Context, fileID string) error {
	var resp apiResponse
	err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/file/delete_file", map[string]any{
		"fileIds": []string{fileID},
	}, &resp, true)
	if err != nil {
		return err
	}
	if !isSuccess(resp) {
		return responseError(resp)
	}
	return nil
}

func rawFileToObj(item map[string]any) model.Obj {
	fileID := firstString(item, "fileId", "id", "fid", "resId")
	name := firstString(item, "fileName", "name", "filename")
	rawType := findValue(item, "type", "resType", "fileType", "dirType")
	isDir := boolFromAny(item["isDir"]) || boolFromAny(item["is_dir"]) || boolFromAny(item["dir"]) || isDirectoryType(rawType)
	if isFileType(rawType) {
		isDir = false
	}
	modTime := parseTime(findValue(item, "utime", "updateTime", "updatedAt", "UpdateAt", "modifyTime", "mtime"))
	size := int64(0)
	if !isDir {
		size = int64(intFromAny(findValue(item, "fileSize", "size", "Size")))
	}
	return &Object{
		Object: model.Object{
			ID:       fileID,
			Name:     name,
			Size:     size,
			Modified: modTime,
			Ctime:    modTime,
			IsFolder: isDir,
		},
		ParentID: firstString(item, "parentId", "parent_file_id", "parentFileId"),
		DriveID:  firstString(item, "gcid", "GCID", "md5"),
	}
}

func isDirectoryType(value any) bool {
	switch v := value.(type) {
	case float64:
		return v == 2
	case int:
		return v == 2
	case int64:
		return v == 2
	case string:
		return v == "2" || v == "dir" || v == "folder"
	default:
		return false
	}
}

func isFileType(value any) bool {
	switch v := value.(type) {
	case float64:
		return v == 0 || v == 1
	case int:
		return v == 0 || v == 1
	case int64:
		return v == 0 || v == 1
	case string:
		return v == "0" || v == "1" || v == "file"
	default:
		return false
	}
}

func isSuccess(resp apiResponse) bool {
	code, hasCode := resp["code"]
	if hasCode {
		switch v := code.(type) {
		case float64:
			if v != 0 && v != 200 {
				return false
			}
		case int:
			if v != 0 && v != 200 {
				return false
			}
		case string:
			if v != "" && v != "0" && v != "200" {
				return false
			}
		}
	}
	msg := strings.ToLower(firstString(resp, "msg", "message", "error"))
	return msg != "error" && msg != "fail" && msg != "failed"
}

func responseError(resp apiResponse) error {
	return fmt.Errorf("guangyapan api error: code=%v msg=%s", resp["code"], firstString(resp, "msg", "message", "error", "error_description"))
}

func extractList(resp apiResponse) []map[string]any {
	data := resp["data"]
	if list, ok := data.([]any); ok {
		return anyListToMapList(list)
	}
	dataObj, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range []string{"list", "files", "items", "records", "fileList", "infoList", "InfoList"} {
		if list, ok := dataObj[key].([]any); ok {
			return anyListToMapList(list)
		}
	}
	return nil
}

func anyListToMapList(list []any) []map[string]any {
	items := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if obj, ok := item.(map[string]any); ok {
			items = append(items, obj)
		}
	}
	return items
}

func dataMap(resp apiResponse) map[string]any {
	if data, ok := resp["data"].(map[string]any); ok {
		return data
	}
	return resp
}

func firstString(data map[string]any, keys ...string) string {
	value := findValue(data, keys...)
	switch v := value.(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func findValue(data map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := data[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		i, _ := strconv.Atoi(v)
		return i
	default:
		return 0
	}
}

func boolFromAny(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

func parseTime(value any) time.Time {
	switch v := value.(type) {
	case float64:
		return unixTime(v)
	case int:
		return unixTime(float64(v))
	case int64:
		return unixTime(float64(v))
	case string:
		if v == "" {
			return time.Now()
		}
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return unixTime(n)
		}
		if t, err := time.Parse(time.RFC3339, utils.SanitizeTimeString(strings.Replace(v, "Z", "+00:00", 1))); err == nil {
			return t
		}
		if t, err := time.Parse("2006-01-02 15:04:05", utils.SanitizeTimeString(v)); err == nil {
			return t
		}
	}
	return time.Now()
}

func unixTime(value float64) time.Time {
	if value > 9999999999 {
		value = value / 1000
	}
	return time.Unix(int64(value), 0)
}
