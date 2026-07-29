package guangyapan

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	netutil "github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

type uploadParams struct {
	Endpoint        string
	BucketName      string
	ObjectPath      string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

var uploadObject = defaultUploadObject

func (d *GuangYaPan) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if dstDir == nil || file == nil {
		return nil, errors.New("dst_dir or file is nil")
	}
	if up == nil {
		up = func(float64) {}
	}

	cached, size, fileMD5, err := cacheAndMD5(file)
	if err != nil {
		return nil, err
	}
	if closer, ok := cached.(io.Closer); ok {
		defer closer.Close()
	}

	parentID := normalizeFileID(dstDir.GetID())
	fileName := file.GetName()
	flashResp, err := d.checkFlashUpload(ctx, "", fileMD5, size, fileName, parentID)
	if err != nil {
		return nil, err
	}
	if isSuccess(flashResp) && flashResp["data"] != nil {
		up(100)
		return uploadedObj(file, fileName, size, parentID, fileMD5, dataMap(flashResp)), nil
	}

	tokenResp, err := d.getUploadToken(ctx, fileName, size, fileMD5, parentID)
	if err != nil {
		return nil, err
	}
	if intFromAny(tokenResp["code"]) == 156 {
		up(100)
		return uploadedObj(file, fileName, size, parentID, fileMD5, dataMap(tokenResp)), nil
	}
	if !isSuccess(tokenResp) {
		return nil, responseError(tokenResp)
	}

	params, err := uploadParamsFromResponse(tokenResp)
	if err != nil {
		return nil, err
	}
	if _, err := cached.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := uploadObject(ctx, params, cached, size, up); err != nil {
		return nil, err
	}
	return uploadedObj(file, fileName, size, parentID, fileMD5, dataMap(tokenResp)), nil
}

func cacheAndMD5(file model.FileStreamer) (model.File, int64, string, error) {
	hash := md5.New()
	var counted int64
	writer := io.MultiWriter(hash, countingWriter{n: &counted})
	cached, err := file.CacheFullAndWriter(nil, writer)
	if err != nil {
		return nil, 0, "", err
	}
	size := file.GetSize()
	if size < 0 || size == 0 && counted > 0 {
		size = counted
	}
	if _, err := cached.Seek(0, io.SeekStart); err != nil {
		return nil, 0, "", err
	}
	return cached, size, strings.ToUpper(hex.EncodeToString(hash.Sum(nil))), nil
}

type countingWriter struct {
	n *int64
}

func (w countingWriter) Write(p []byte) (int, error) {
	*w.n += int64(len(p))
	return len(p), nil
}

func (d *GuangYaPan) checkFlashUpload(ctx context.Context, taskID, fileMD5 string, fileSize int64, fileName, parentID string) (apiResponse, error) {
	var resp apiResponse
	err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/check_can_flash_upload", map[string]any{
		"taskId":   taskID,
		"gcid":     fileMD5,
		"fileSize": fileSize,
		"name":     fileName,
		"parentId": parentID,
	}, &resp, true)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (d *GuangYaPan) getUploadToken(ctx context.Context, fileName string, fileSize int64, fileMD5, parentID string) (apiResponse, error) {
	var resp apiResponse
	err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizuserres.s/v1/get_res_center_token", map[string]any{
		"capacity": 2,
		"name":     fileName,
		"parentId": parentID,
		"res": map[string]any{
			"fileSize": fileSize,
			"md5":      fileMD5,
		},
	}, &resp, true)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func uploadParamsFromResponse(resp apiResponse) (uploadParams, error) {
	data := dataMap(resp)
	creds, _ := findValue(data, "creds", "credentials").(map[string]any)
	params := uploadParams{
		Endpoint:        normalizeOSSEndpoint(firstString(data, "fullEndPoint", "endPoint", "endpoint")),
		BucketName:      firstString(data, "bucketName", "bucket"),
		ObjectPath:      firstString(data, "objectPath", "objectKey", "key"),
		AccessKeyID:     firstString(creds, "accessKeyID", "accessKeyId", "AccessKeyId"),
		SecretAccessKey: firstString(creds, "secretAccessKey", "accessKeySecret", "AccessKeySecret"),
		SessionToken:    firstString(creds, "sessionToken", "securityToken", "SecurityToken"),
	}
	if params.Endpoint == "" || params.BucketName == "" || params.ObjectPath == "" ||
		params.AccessKeyID == "" || params.SecretAccessKey == "" || params.SessionToken == "" {
		return uploadParams{}, fmt.Errorf("incomplete upload token response")
	}
	return params, nil
}

func normalizeOSSEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	return "https://" + endpoint
}

func defaultUploadObject(ctx context.Context, params uploadParams, file model.File, size int64, up func(float64)) error {
	client, err := netutil.NewOSSClient(endpointForOSSClient(params.Endpoint, params.BucketName), params.AccessKeyID, params.SecretAccessKey, oss.SecurityToken(params.SessionToken))
	if err != nil {
		return err
	}
	bucket, err := client.Bucket(params.BucketName)
	if err != nil {
		return err
	}
	reader := driver.NewLimitedUploadFile(ctx, file)
	if up == nil {
		up = func(float64) {}
	}
	if size > 0 {
		return bucket.PutObject(params.ObjectPath, io.TeeReader(reader, driver.NewProgress(size, up)))
	}
	err = bucket.PutObject(params.ObjectPath, reader)
	if err == nil {
		up(100)
	}
	return err
}

func endpointForOSSClient(endpoint, bucketName string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || bucketName == "" {
		return endpoint
	}
	prefix := bucketName + "."
	if strings.HasPrefix(parsed.Host, prefix) {
		parsed.Host = strings.TrimPrefix(parsed.Host, prefix)
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func uploadedObj(file model.FileStreamer, name string, size int64, parentID string, fileMD5 string, raw map[string]any) model.Obj {
	modTime := file.ModTime()
	if modTime.IsZero() {
		modTime = time.Now()
	}
	createTime := file.CreateTime()
	if createTime.IsZero() {
		createTime = modTime
	}
	return &Object{
		Object: model.Object{
			ID:       firstString(raw, "fileId", "id", "fid", "resId"),
			Name:     name,
			Size:     size,
			Modified: modTime,
			Ctime:    createTime,
			IsFolder: false,
			HashInfo: utils.NewHashInfo(utils.MD5, strings.ToLower(fileMD5)),
		},
		ParentID: parentID,
		DriveID:  firstString(raw, "gcid", "GCID", "md5"),
	}
}
