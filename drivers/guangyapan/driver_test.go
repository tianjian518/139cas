package guangyapan

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/go-resty/resty/v2"
)

func TestListConvertsGuangYaItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
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
	}))
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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

func TestPutUploadsViaTokenAndOSS(t *testing.T) {
	content := []byte("hello")
	var checkedFlash, requestedToken bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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

func newTestDriver(t *testing.T) *GuangYaPan {
	t.Helper()
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
