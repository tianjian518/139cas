package guangyapan

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/go-resty/resty/v2"
)

func TestListConvertsGuangYaItems(t *testing.T) {
	server := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/user/me" {
			writeJSON(t, w, map[string]any{"sub": "user-1"})
			return
		}
		if r.URL.Path != "/nd.bizuserres.s/v1/file/get_file_list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization header = %q", got)
		}
		writeJSON(t, w, map[string]any{
			"code": 0,
			"data": map[string]any{
				"total": 2,
				"list": []map[string]any{
					{
						"fileId":     "dir-1",
						"parentId":   "",
						"fileName":   "Movies",
						"type":       2,
						"updateTime": "2026-05-26T12:00:00Z",
					},
					{
						"fileId":     "file-1",
						"parentId":   "",
						"fileName":   "demo.mkv",
						"type":       1,
						"fileSize":   734003200,
						"updateTime": "2026-05-26T12:30:00Z",
						"gcid":       "gcid-file-1",
					},
				},
			},
		})
	})
	defer server.Close()

	oldAccountBaseURL, oldAPIBaseURL := accountBaseURL, apiBaseURL
	accountBaseURL, apiBaseURL = server.URL, server.URL
	defer func() {
		accountBaseURL, apiBaseURL = oldAccountBaseURL, oldAPIBaseURL
	}()

	driver := &GuangYaPan{
		Addition: Addition{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ClientID:     defaultClientID,
			DeviceID:     testDeviceID,
			PageSize:     100,
			OrderBy:      3,
			SortType:     1,
		},
		client: resty.New(),
	}
	if err := driver.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	objs, err := driver.List(context.Background(), &model.Object{ID: "", Name: "root", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("len(objs) = %d", len(objs))
	}
	if objs[0].GetID() != "dir-1" || objs[0].GetName() != "Movies" || !objs[0].IsDir() {
		t.Fatalf("first object = %#v", objs[0])
	}
	if objs[1].GetID() != "file-1" || objs[1].GetName() != "demo.mkv" || objs[1].IsDir() {
		t.Fatalf("second object = %#v", objs[1])
	}
	if objs[1].GetSize() != 734003200 {
		t.Fatalf("file size = %d", objs[1].GetSize())
	}
	if want := time.Date(2026, 5, 26, 12, 30, 0, 0, time.UTC); !objs[1].ModTime().Equal(want) {
		t.Fatalf("file mod time = %s", objs[1].ModTime())
	}
}

func TestRawFileToObjRecognizesAlternateDirectoryTypeFields(t *testing.T) {
	cases := []map[string]any{
		{
			"fileId":   "dir-res-type",
			"fileName": "ByResType",
			"resType":  2,
		},
		{
			"fileId":   "dir-file-type",
			"fileName": "ByFileType",
			"fileType": 2,
		},
		{
			"fileId":   "dir-dir-type",
			"fileName": "ByDirType",
			"dirType":  2,
		},
	}

	for _, item := range cases {
		obj := rawFileToObj(item)
		if !obj.IsDir() {
			t.Fatalf("%s should be recognized as directory", obj.GetName())
		}
	}
}

func TestLinkFallsBackToVodDownloadURL(t *testing.T) {
	var vodCalled bool
	server := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/user/me":
			writeJSON(t, w, map[string]any{"sub": "user-1"})
		case "/nd.bizuserres.s/v1/get_res_download_url":
			writeJSON(t, w, map[string]any{"code": 143, "msg": "use vod"})
		case "/nd.bizuserres.s/v1/file/get_vod_download_url":
			vodCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if body["fileId"] != "file-1" || body["gcid"] != "gcid-file-1" {
				t.Fatalf("vod request body = %#v", body)
			}
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{"signedURL": "https://media.example/demo.mkv"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	defer server.Close()

	oldAccountBaseURL, oldAPIBaseURL := accountBaseURL, apiBaseURL
	accountBaseURL, apiBaseURL = server.URL, server.URL
	defer func() {
		accountBaseURL, apiBaseURL = oldAccountBaseURL, oldAPIBaseURL
	}()

	driver := &GuangYaPan{
		Addition: Addition{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ClientID:     defaultClientID,
			DeviceID:     "device-id",
		},
		client: resty.New(),
	}
	if err := driver.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	link, err := driver.Link(context.Background(), &Object{Object: model.Object{
		ID:       "file-1",
		Name:     "demo.mkv",
		IsFolder: false,
	}, DriveID: "gcid-file-1"}, model.LinkArgs{})
	if err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	if !vodCalled {
		t.Fatal("expected VOD fallback endpoint to be called")
	}
	if link.URL != "https://media.example/demo.mkv" {
		t.Fatalf("link.URL = %q", link.URL)
	}
}

func TestRemoveCallsDeleteFileEndpoint(t *testing.T) {
	var deleteCalled bool
	server := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/user/me" {
			writeJSON(t, w, map[string]any{"sub": "user-1"})
			return
		}
		if r.URL.Path != "/nd.bizuserres.s/v1/file/delete_file" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization header = %q", got)
		}
		var body map[string][]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if got := body["fileIds"]; len(got) != 1 || got[0] != "file-1" {
			t.Fatalf("delete request fileIds = %#v", got)
		}
		deleteCalled = true
		writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
	})
	defer server.Close()

	oldAccountBaseURL, oldAPIBaseURL := accountBaseURL, apiBaseURL
	accountBaseURL, apiBaseURL = server.URL, server.URL
	defer func() {
		accountBaseURL, apiBaseURL = oldAccountBaseURL, oldAPIBaseURL
	}()

	driver := &GuangYaPan{
		Addition: Addition{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ClientID:     defaultClientID,
			DeviceID:     "device-id",
		},
		client: resty.New(),
	}
	if err := driver.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	err := driver.Remove(context.Background(), &Object{Object: model.Object{
		ID:       "file-1",
		Name:     "demo.mkv",
		IsFolder: false,
	}})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if !deleteCalled {
		t.Fatal("expected delete endpoint to be called")
	}
}

func TestMoveCallsMoveFileEndpoint(t *testing.T) {
	server := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nd.bizuserres.s/v1/file/move_file" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		assertStringList(t, body["fileIds"], "file-1")
		if body["parentId"] != "dst-dir" {
			t.Fatalf("parentId = %#v", body["parentId"])
		}
		writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
	})
	defer server.Close()
	withTestBaseURLs(t, server.URL)

	driver := newTestDriver(t)
	err := driver.Move(context.Background(),
		&Object{Object: model.Object{ID: "file-1", Name: "demo.mkv"}},
		&Object{Object: model.Object{ID: "dst-dir", Name: "Movies", IsFolder: true}},
	)
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
}

func TestCopyCallsCopyFileEndpoint(t *testing.T) {
	server := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nd.bizuserres.s/v1/file/copy_file" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		assertStringList(t, body["fileIds"], "file-1")
		if body["parentId"] != "dst-dir" {
			t.Fatalf("parentId = %#v", body["parentId"])
		}
		writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
	})
	defer server.Close()
	withTestBaseURLs(t, server.URL)

	driver := newTestDriver(t)
	err := driver.Copy(context.Background(),
		&Object{Object: model.Object{ID: "file-1", Name: "demo.mkv"}},
		&Object{Object: model.Object{ID: "dst-dir", Name: "Movies", IsFolder: true}},
	)
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
}

func TestRenameCallsRenameEndpoint(t *testing.T) {
	server := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nd.bizuserres.s/v1/file/rename" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["fileId"] != "file-1" || body["newName"] != "renamed.mkv" {
			t.Fatalf("rename request body = %#v", body)
		}
		writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
	})
	defer server.Close()
	withTestBaseURLs(t, server.URL)

	driver := newTestDriver(t)
	err := driver.Rename(context.Background(), &Object{Object: model.Object{
		ID:   "file-1",
		Name: "demo.mkv",
	}}, "renamed.mkv")
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
}

func TestMakeDirCallsCreateDirEndpoint(t *testing.T) {
	server := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nd.bizuserres.s/v1/file/create_dir" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["parentId"] != "parent-dir" || body["dirName"] != "New Folder" || body["failIfNameExist"] != true {
			t.Fatalf("create dir request body = %#v", body)
		}
		writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
	})
	defer server.Close()
	withTestBaseURLs(t, server.URL)

	driver := newTestDriver(t)
	err := driver.MakeDir(context.Background(), &Object{Object: model.Object{
		ID:       "parent-dir",
		Name:     "Parent",
		IsFolder: true,
	}}, "New Folder")
	if err != nil {
		t.Fatalf("MakeDir() error = %v", err)
	}
}

func TestConfigAllowsUpload(t *testing.T) {
	if config.NoUpload {
		t.Fatal("GuangYaPan should allow upload after Put is implemented")
	}
}

// TestConfigExposesSMSLoginFields 保单里确实暴露了官方的短信登录字段。
func TestConfigExposesSMSLoginFields(t *testing.T) {
	additionType := reflect.TypeOf(Addition{})
	for _, field := range []string{
		"phone_number", "verify_code", "send_code",
		"captcha_token", "verification_id",
		"access_token", "refresh_token",
	} {
		found := false
		for i := 0; i < additionType.NumField(); i++ {
			tag := additionType.Field(i).Tag.Get("json")
			if strings.Split(tag, ",")[0] == field {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Addition 缺少字段 %q（官方驱动的短信登录字段）", field)
		}
	}
}

// TestPhoneNumberNormalization 校验手机号归一化逻辑。
func TestPhoneNumberNormalization(t *testing.T) {
	cases := map[string]string{
		"13800000000":     "+86 13800000000",
		"+8613800000000":  "+86 13800000000",
		"+86 13800000000": "+86 13800000000",
		"138 0000 0000":   "+86 13800000000",
	}
	for in, want := range cases {
		if got := normalizePhoneE164(in); got != want {
			t.Fatalf("normalizePhoneE164(%q) = %q, want %q", in, got, want)
		}
	}
	if got := normalizeCaptchaUsername("+8613800000000"); got != "13800000000" {
		t.Fatalf("normalizeCaptchaUsername = %q", got)
	}
}

// TestSMSLoginTwoStage 模拟官方两段式短信登录：
// 第一段 send_code 拿 verification_id，第二段 verify_code 换令牌。
func TestSMSLoginTwoStage(t *testing.T) {
	// 测试环境没有数据库，替换掉落库逻辑。
	oldPersist := persistAddition
	persistAddition = func(*GuangYaPan) {}
	t.Cleanup(func() { persistAddition = oldPersist })

	var captchaInitCalled, verificationCalled, verifyCalled, signinCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/shield/captcha/init":
			captchaInitCalled = true
			writeJSON(t, w, map[string]any{"captcha_token": "captcha-token-1"})
		case "/v1/auth/verification":
			verificationCalled = true
			if got := r.Header.Get("X-Captcha-Token"); got != "captcha-token-1" {
				t.Fatalf("X-Captcha-Token = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode verification body: %v", err)
			}
			if body["phone_number"] != "+86 13800000000" {
				t.Fatalf("phone_number = %#v", body["phone_number"])
			}
			writeJSON(t, w, map[string]any{"verification_id": "vid-1"})
		case "/v1/auth/verification/verify":
			verifyCalled = true
			writeJSON(t, w, map[string]any{"verification_token": "vtoken-1"})
		case "/v1/auth/signin":
			signinCalled = true
			writeJSON(t, w, map[string]any{
				"access_token":  "at-1",
				"refresh_token": "rt-1",
			})
		case "/v1/user/me":
			writeJSON(t, w, map[string]any{"sub": "user-1"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	withTestBaseURLs(t, server.URL)

	// 第一段：只填手机号 + 勾选发码
	d := &GuangYaPan{Addition: Addition{
		ClientID:    defaultClientID,
		DeviceID:    testDeviceID,
		PhoneNumber: "13800000000",
		SendCode:    true,
	}}
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("第一段 Init() error = %v", err)
	}
	if !captchaInitCalled || !verificationCalled {
		t.Fatalf("第一段未走完整流程: captchaInit=%v verification=%v", captchaInitCalled, verificationCalled)
	}
	if d.VerificationID != "vid-1" {
		t.Fatalf("VerificationID = %q", d.VerificationID)
	}
	if d.SendCode {
		t.Fatal("SendCode 发送后应自动重置为 false")
	}

	// 第二段：填验证码完成登录
	d.VerifyCode = "123456"
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("第二段 Init() error = %v", err)
	}
	if !verifyCalled || !signinCalled {
		t.Fatalf("第二段未走完整流程: verify=%v signin=%v", verifyCalled, signinCalled)
	}
	if d.AccessToken != "at-1" || d.RefreshToken != "rt-1" {
		t.Fatalf("令牌未写入: at=%q rt=%q", d.AccessToken, d.RefreshToken)
	}
	if d.VerifyCode != "" {
		t.Fatal("登录成功后一次性验证码应被清空")
	}
}

func TestPutUploadsViaTokenAndOSS(t *testing.T) {
	content := []byte("hello")
	var checkedFlash, requestedToken bool
	server := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nd.bizuserres.s/v1/check_can_flash_upload":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode flash request body: %v", err)
			}
			if body["taskId"] != "" || body["gcid"] != "5D41402ABC4B2A76B9719D911017C592" || body["name"] != "hello.txt" || body["parentId"] != "parent-dir" {
				t.Fatalf("flash request body = %#v", body)
			}
			if body["fileSize"] != float64(len(content)) {
				t.Fatalf("flash fileSize = %#v", body["fileSize"])
			}
			checkedFlash = true
			writeJSON(t, w, map[string]any{"code": 0, "data": nil})
		case "/nd.bizuserres.s/v1/get_res_center_token":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode token request body: %v", err)
			}
			if body["capacity"] != float64(2) || body["name"] != "hello.txt" || body["parentId"] != "parent-dir" {
				t.Fatalf("upload token request body = %#v", body)
			}
			res, ok := body["res"].(map[string]any)
			if !ok {
				t.Fatalf("res is %T", body["res"])
			}
			if res["fileSize"] != float64(len(content)) || res["md5"] != "5D41402ABC4B2A76B9719D911017C592" {
				t.Fatalf("upload token res = %#v", res)
			}
			requestedToken = true
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"objectPath":   "objects/hello.txt",
					"bucketName":   "bucket-a",
					"fullEndPoint": "https://bucket-a.oss-cn-test.aliyuncs.com",
					"creds": map[string]any{
						"accessKeyID":     "ak",
						"secretAccessKey": "sk",
						"sessionToken":    "token",
					},
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	defer server.Close()
	withTestBaseURLs(t, server.URL)

	var uploaded bool
	oldUploadObject := uploadObject
	uploadObject = func(ctx context.Context, params uploadParams, file model.File, size int64, up func(float64)) error {
		if params.Endpoint != "https://bucket-a.oss-cn-test.aliyuncs.com" || params.BucketName != "bucket-a" || params.ObjectPath != "objects/hello.txt" {
			t.Fatalf("upload params = %#v", params)
		}
		if params.AccessKeyID != "ak" || params.SecretAccessKey != "sk" || params.SessionToken != "token" {
			t.Fatalf("upload creds = %#v", params)
		}
		if size != int64(len(content)) {
			t.Fatalf("upload size = %d", size)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			t.Fatalf("seek upload file: %v", err)
		}
		got, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read upload file: %v", err)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("upload content = %q", got)
		}
		up(100)
		uploaded = true
		return nil
	}
	t.Cleanup(func() {
		uploadObject = oldUploadObject
	})

	driver := newTestDriver(t)
	var progress float64
	obj, err := driver.Put(context.Background(), &Object{Object: model.Object{
		ID:       "parent-dir",
		Name:     "Parent",
		IsFolder: true,
	}}, &streamPkg.FileStream{
		Obj:      &model.Object{Name: "hello.txt", Size: int64(len(content))},
		Reader:   bytes.NewReader(content),
		Mimetype: "text/plain",
	}, func(p float64) {
		progress = p
	})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if obj == nil || obj.GetName() != "hello.txt" || obj.GetSize() != int64(len(content)) || obj.IsDir() {
		t.Fatalf("Put() obj = %#v", obj)
	}
	if !checkedFlash || !requestedToken || !uploaded {
		t.Fatalf("checkedFlash=%v requestedToken=%v uploaded=%v", checkedFlash, requestedToken, uploaded)
	}
	if progress != 100 {
		t.Fatalf("progress = %f", progress)
	}
}

func TestEndpointForOSSClientStripsBucketHost(t *testing.T) {
	endpoint := endpointForOSSClient("https://bucket-a.oss-cn-test.aliyuncs.com", "bucket-a")
	if endpoint != "https://oss-cn-test.aliyuncs.com" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}

const testDeviceID = "0123456789abcdef0123456789abcdef"

func newTestDriver(t *testing.T) *GuangYaPan {
	t.Helper()
	driver := &GuangYaPan{
		Addition: Addition{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ClientID:     defaultClientID,
			DeviceID:     testDeviceID,
		},
		client: resty.New(),
	}
	if err := driver.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	return driver
}

func withTestBaseURLs(t *testing.T, url string) {
	t.Helper()
	oldAccountBaseURL, oldAPIBaseURL := accountBaseURL, apiBaseURL
	accountBaseURL, apiBaseURL = url, url
	t.Cleanup(func() {
		accountBaseURL, apiBaseURL = oldAccountBaseURL, oldAPIBaseURL
	})
}

func assertStringList(t *testing.T, value any, want ...string) {
	t.Helper()
	got, ok := value.([]any)
	if !ok {
		t.Fatalf("value is %T, want []any", value)
	}
	if len(got) != len(want) {
		t.Fatalf("len(value) = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value[%d] = %#v, want %q", i, got[i], want[i])
		}
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// testServer 包一层 httptest.Server，自动应答账号域登录态校验
// （Init 现在会先校验 access_token，测试里无需每个用例单独处理）。
func testServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/user/me":
			writeJSON(t, w, map[string]any{"sub": "user-1"})
		case "/v1/auth/token":
			writeJSON(t, w, map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
			})
		default:
			handler(w, r)
		}
	}))
}
