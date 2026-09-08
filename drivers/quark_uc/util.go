package quark

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/cookie"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

// do others that not defined in Driver interface

func (d *QuarkOrUC) request(pathname string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	u := d.conf.api + pathname
	req := base.RestyClient.R()
	req.SetHeaders(map[string]string{
		"Cookie":  d.Cookie,
		"Accept":  "application/json, text/plain, */*",
		"Referer": d.conf.referer,
	})
	req.SetQueryParam("pr", d.conf.pr)
	req.SetQueryParam("fr", "pc")
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e Resp
	req.SetError(&e)
	res, err := req.Execute(method, u)
	if err != nil {
		return nil, err
	}
	__puus := cookie.GetCookie(res.Cookies(), "__puus")
	if __puus != nil {
		d.Cookie = cookie.SetStr(d.Cookie, "__puus", __puus.Value)
		op.MustSaveDriverStorage(d)
	}

	if d.UseTransCodingAddress && d.config.Name == "Quark" {
		__pus := cookie.GetCookie(res.Cookies(), "__pus")
		if __pus != nil {
			d.Cookie = cookie.SetStr(d.Cookie, "__pus", __pus.Value)
			op.MustSaveDriverStorage(d)
		}
	}

	if e.Status >= 400 || e.Code != 0 {
		return nil, errors.New(e.Message)
	}
	return res.Body(), nil
}

func (d *QuarkOrUC) GetFiles(parent string) ([]model.Obj, error) {
	files := make([]model.Obj, 0)
	page := 1
	size := 100
	query := map[string]string{
		"pdir_fid":             parent,
		"_size":                strconv.Itoa(size),
		"_fetch_total":         "1",
		"fetch_all_file":       "1",
		"fetch_risk_file_name": "1",
	}
	if d.OrderBy != "none" {
		query["_sort"] = "file_type:asc," + d.OrderBy + ":" + d.OrderDirection
	}
	for {
		query["_page"] = strconv.Itoa(page)
		var resp SortResp
		_, err := d.request("/file/sort", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(query)
		}, &resp)
		if err != nil {
			return nil, err
		}
		for _, file := range resp.Data.List {
			file.FileName = html.UnescapeString(file.FileName)
			if d.OnlyListVideoFile {
				// 开启后 只列出视频文件和文件夹
				if file.IsDir() || file.Category == 1 {
					files = append(files, &file)
				}
			} else {
				files = append(files, &file)
			}
		}

		if page*size >= resp.Metadata.Total {
			break
		}
		page++
	}

	return files, nil
}

func (d *QuarkOrUC) getDownloadLink(file model.Obj) (*model.Link, error) {
	data := base.Json{
		"fids": []string{file.GetID()},
	}
	var resp DownResp
	ua := d.conf.ua
	_, err := d.request("/file/download", http.MethodPost, func(req *resty.Request) {
		req.SetHeader("User-Agent", ua).
			SetBody(data)
	}, &resp)
	if err != nil {
		return nil, err
	}

	return &model.Link{
		URL: resp.Data[0].DownloadUrl,
		Header: http.Header{
			"Cookie":     []string{d.Cookie},
			"Referer":    []string{d.conf.referer},
			"User-Agent": []string{ua},
		},
		Concurrency: 3,
		PartSize:    10 * utils.MB,
	}, nil
}

func (d *QuarkOrUC) getTranscodingLink(file model.Obj) (*model.Link, error) {
	data := base.Json{
		"fid":         file.GetID(),
		"resolutions": "low,normal,high,super,2k,4k",
		"supports":    "fmp4_av,m3u8,dolby_vision",
	}
	var resp TranscodingResp
	ua := d.conf.ua

	_, err := d.request("/file/v2/play/project", http.MethodPost, func(req *resty.Request) {
		req.SetHeader("User-Agent", ua).
			SetBody(data)
	}, &resp)
	if err != nil {
		return nil, err
	}

	for _, info := range resp.Data.VideoList {
		if info.VideoInfo.URL != "" {
			return &model.Link{
				URL:           info.VideoInfo.URL,
				ContentLength: info.VideoInfo.Size,
				Concurrency:   3,
				PartSize:      10 * utils.MB,
			}, nil
		}
	}

	return nil, errors.New("no link found")
}

// getPlayLink 走网页端播放器同款的原画播放接口 /file/v2/play。
// 与 download 接口不同，它返回的 CDN 直链无需附加 Cookie/Referer/UA 头，
// 因此 OpenList 可以直接 302 给播放器，客户端直连夸克 CDN 而不经过本机代理。
// 注意：该行为未被官方文档保证，返回的 URL 是否真的免 Cookie 需实测，
// 所以由 UsePlayDirectLink 开关控制，默认关闭。
func (d *QuarkOrUC) getPlayLink(ctx context.Context, file model.Obj) (*model.Link, error) {
	data := base.Json{
		"fid":         file.GetID(),
		"resolutions": "low,normal,high,super,2k,4k",
		"supports":    "fmp4_av,m3u8",
	}
	var resp TranscodingResp
	_, err := d.request("/file/v2/play", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetContext(ctx)
	}, &resp)
	if err != nil {
		return nil, err
	}

	// 只接受 resolution == "default"（原画 = 原文件字节）。
	// 其他档位是转码流，画质有损；宁可报错回退到下载直链（代理播放原画），
	// 也不能让用户在不知情中看低画质。
	for _, info := range resp.Data.VideoList {
		if info.VideoInfo.URL == "" {
			continue
		}
		if info.Resolution != "default" && info.Resolution != "" {
			continue
		}
		size := info.VideoInfo.Size
		if size <= 0 {
			size = file.GetSize()
		}
		// 原画字节数应与文件大小一致；差异过大说明实际返回的是转码档
		if file.GetSize() > 0 && size > 0 {
			diff := size - file.GetSize()
			if diff < 0 {
				diff = -diff
			}
			if diff*100 > file.GetSize() { // >1% 视为非原画
				return nil, fmt.Errorf("play link is not the original quality (size %d != %d)", size, file.GetSize())
			}
		}
		return &model.Link{
			URL:           info.VideoInfo.URL,
			ContentLength: size,
			Concurrency:   3,
			PartSize:      10 * utils.MB,
		}, nil
	}

	return nil, errors.New("no original-quality play link found")
}

func (d *QuarkOrUC) upPre(file model.FileStreamer, parentId string) (UpPreResp, error) {
	now := time.Now()
	data := base.Json{
		"ccp_hash_update": true,
		"dir_name":        "",
		"file_name":       file.GetName(),
		"format_type":     file.GetMimetype(),
		"l_created_at":    now.UnixMilli(),
		"l_updated_at":    now.UnixMilli(),
		"pdir_fid":        parentId,
		"size":            file.GetSize(),
		//"same_path_reuse": true,
	}
	var resp UpPreResp
	_, err := d.request("/file/upload/pre", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, &resp)
	return resp, err
}

func (d *QuarkOrUC) upHash(md5, sha1, taskId string) (bool, error) {
	resp, err := d.upHashResp(md5, sha1, taskId)
	if err != nil {
		return false, err
	}
	return resp.Data.Finish, nil
}

func (d *QuarkOrUC) upHashResp(md5, sha1, taskId string) (*HashResp, error) {
	data := base.Json{
		"md5":     md5,
		"sha1":    sha1,
		"task_id": taskId,
	}
	log.Debugf("hash: %+v", data)
	var resp HashResp
	_, err := d.request("/file/update/hash", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, &resp)
	return &resp, err
}

func (d *QuarkOrUC) upPart(ctx context.Context, pre UpPreResp, mineType string, partNumber int, bytes io.Reader) (string, error) {
	// func (driver QuarkOrUC) UpPart(pre UpPreResp, mineType string, partNumber int, bytes []byte, account *model.Account, md5Str, sha1Str string) (string, error) {
	timeStr := time.Now().UTC().Format(http.TimeFormat)
	data := base.Json{
		"auth_info": pre.Data.AuthInfo,
		"auth_meta": fmt.Sprintf(`PUT

%s
%s
x-oss-date:%s
x-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit
/%s/%s?partNumber=%d&uploadId=%s`,
			mineType, timeStr, timeStr, pre.Data.Bucket, pre.Data.ObjKey, partNumber, pre.Data.UploadId),
		"task_id": pre.Data.TaskId,
	}
	var resp UpAuthResp
	_, err := d.request("/file/upload/auth", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetContext(ctx)
	}, &resp)
	if err != nil {
		return "", err
	}
	//if partNumber == 1 {
	//	finish, err := driver.UpHash(md5Str, sha1Str, pre.Data.TaskId, account)
	//	if err != nil {
	//		return "", err
	//	}
	//	if finish {
	//		return "finish", nil
	//	}
	//}
	u := fmt.Sprintf("https://%s.%s/%s", pre.Data.Bucket, pre.Data.UploadUrl[7:], pre.Data.ObjKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", resp.Data.AuthKey)
	req.Header.Set("Content-Type", mineType)
	req.Header.Set("Referer", "https://pan.quark.cn/")
	req.Header.Set("x-oss-date", timeStr)
	req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
	q := req.URL.Query()
	q.Add("partNumber", strconv.Itoa(partNumber))
	q.Add("uploadId", pre.Data.UploadId)
	req.URL.RawQuery = q.Encode()
	res, err := base.HttpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		respBody, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("up status: %d, error: %s", res.StatusCode, string(respBody))
	}
	return res.Header.Get("Etag"), nil
}

func (d *QuarkOrUC) upCommit(pre UpPreResp, md5s []string) error {
	timeStr := time.Now().UTC().Format(http.TimeFormat)
	log.Debugf("md5s: %+v", md5s)
	bodyBuilder := strings.Builder{}
	bodyBuilder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<CompleteMultipartUpload>
`)
	for i, m := range md5s {
		bodyBuilder.WriteString(fmt.Sprintf(`<Part>
<PartNumber>%d</PartNumber>
<ETag>%s</ETag>
</Part>
`, i+1, m))
	}
	bodyBuilder.WriteString("</CompleteMultipartUpload>")
	body := bodyBuilder.String()
	m := md5.New()
	m.Write([]byte(body))
	contentMd5 := base64.StdEncoding.EncodeToString(m.Sum(nil))
	callbackBytes, err := utils.Json.Marshal(pre.Data.Callback)
	if err != nil {
		return err
	}
	callbackBase64 := base64.StdEncoding.EncodeToString(callbackBytes)
	data := base.Json{
		"auth_info": pre.Data.AuthInfo,
		"auth_meta": fmt.Sprintf(`POST
%s
application/xml
%s
x-oss-callback:%s
x-oss-date:%s
x-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit
/%s/%s?uploadId=%s`,
			contentMd5, timeStr, callbackBase64, timeStr,
			pre.Data.Bucket, pre.Data.ObjKey, pre.Data.UploadId),
		"task_id": pre.Data.TaskId,
	}
	log.Debugf("xml: %s", body)
	log.Debugf("auth data: %+v", data)
	var resp UpAuthResp
	_, err = d.request("/file/upload/auth", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, &resp)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("https://%s.%s/%s", pre.Data.Bucket, pre.Data.UploadUrl[7:], pre.Data.ObjKey)
	res, err := base.RestyClient.R().
		SetHeaders(map[string]string{
			"Authorization":    resp.Data.AuthKey,
			"Content-MD5":      contentMd5,
			"Content-Type":     "application/xml",
			"Referer":          "https://pan.quark.cn/",
			"x-oss-callback":   callbackBase64,
			"x-oss-date":       timeStr,
			"x-oss-user-agent": "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit",
		}).
		SetQueryParams(map[string]string{
			"uploadId": pre.Data.UploadId,
		}).SetBody(body).Post(u)
	if err != nil {
		return err
	}
	if res.StatusCode() != 200 {
		return fmt.Errorf("up status: %d, error: %s", res.StatusCode(), res.String())
	}
	return nil
}

func (d *QuarkOrUC) upFinish(pre UpPreResp) error {
	data := base.Json{
		"obj_key": pre.Data.ObjKey,
		"task_id": pre.Data.TaskId,
	}
	_, err := d.request("/file/upload/finish", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	if err != nil {
		return err
	}
	time.Sleep(time.Second)
	return nil
}

func (d *QuarkOrUC) memberInfo(ctx context.Context) (*MemberResp, error) {
	var resp MemberResp
	query := map[string]string{
		"fetch_subscribe": "false",
		"_ch":             "home",
		"fetch_identity":  "false",
	}
	_, err := d.request("/member", http.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(query)
		req.SetContext(ctx)
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
