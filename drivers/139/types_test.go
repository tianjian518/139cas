package _139

import (
	"context"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestPersonalFileItemHashInfoUsesSHA256ContentHash(t *testing.T) {
	item := PersonalFileItem{
		ContentHash:          "df5e2a9e39493efcc9ab18f571d8a80516652b5295fc5a14c6fe5aba429365cc",
		ContentHashAlgorithm: "sha256",
	}

	got := item.HashInfo().GetHash(utils.SHA256)
	if got != item.ContentHash {
		t.Fatalf("SHA256 hash = %q, want %q", got, item.ContentHash)
	}
}

func TestPersonalFileItemHashInfoIgnoresNonSHA256(t *testing.T) {
	item := PersonalFileItem{
		ContentHash:          "0123456789abcdef0123456789abcdef",
		ContentHashAlgorithm: "md5",
	}

	if got := item.HashInfo().GetHash(utils.SHA256); got != "" {
		t.Fatalf("SHA256 hash = %q, want empty", got)
	}
}

func TestShouldRestoreCASWithPCHeadersDefaultsToTrueForExistingStorage(t *testing.T) {
	cases := []Yun139{
		{},
		{Storage: model.Storage{Addition: `{}`}},
		{Storage: model.Storage{Addition: `{"restore_source_from_cas":true}`}},
		{Storage: model.Storage{Addition: `{invalid-json`}},
	}

	for _, d := range cases {
		if !d.shouldRestoreCASWithPCHeaders() {
			t.Fatalf("shouldRestoreCASWithPCHeaders() = false, want true for addition %q", d.Storage.Addition)
		}
	}
}

func TestShouldRestoreCASWithPCHeadersHonorsExplicitFalse(t *testing.T) {
	d := Yun139{Storage: model.Storage{Addition: `{"cas_restore_use_pc_headers":false}`}}

	if d.shouldRestoreCASWithPCHeaders() {
		t.Fatal("shouldRestoreCASWithPCHeaders() = true, want false")
	}
}

func TestApplyCASRestorePCHeadersDefaultPreservesExplicitFalse(t *testing.T) {
	d := Yun139{Storage: model.Storage{Addition: `{"cas_restore_use_pc_headers":false}`}}

	d.applyCASRestorePCHeadersDefault()

	if d.CASRestoreUsePCHeaders {
		t.Fatal("CASRestoreUsePCHeaders = true, want false")
	}
}

func TestApplyCASRestorePCHeadersDefaultEnablesOldStorage(t *testing.T) {
	d := Yun139{Storage: model.Storage{Addition: `{"restore_source_from_cas":true}`}}

	d.applyCASRestorePCHeadersDefault()

	if !d.CASRestoreUsePCHeaders {
		t.Fatal("CASRestoreUsePCHeaders = false, want true")
	}
}

func TestPCPersonalHeadersUsePCMarkers(t *testing.T) {
	d := Yun139{
		Account: "13800138000",
		Addition: Addition{
			UserDomainID: "fake-domain-id",
		},
	}

	headers := d.pcPersonalHeaders("{}", "2026-05-27 00:00:00", "abcdefghijklmnop", "SIGN", "1")

	if headers["x-yun-app-channel"] != pcAppChannel {
		t.Fatalf("x-yun-app-channel = %q, want %q", headers["x-yun-app-channel"], pcAppChannel)
	}
	if headers["x-huawei-channelSrc"] != pcAppChannel {
		t.Fatalf("x-huawei-channelSrc = %q, want %q", headers["x-huawei-channelSrc"], pcAppChannel)
	}
	if headers["x-yun-device-id"] == "" {
		t.Fatal("x-yun-device-id is empty")
	}
	if headers["x-yun-device-info"] != d.pcDeviceInfo() {
		t.Fatalf("x-yun-device-info = %q, want pcDeviceInfo()", headers["x-yun-device-info"])
	}
	if headers["x-yun-uni"] != "fake-domain-id" {
		t.Fatalf("x-yun-uni = %q, want fake-domain-id", headers["x-yun-uni"])
	}
	if headers["x-ExpRoute-Code"] != "routeCode=13800138000,type=2" {
		t.Fatalf("x-ExpRoute-Code = %q", headers["x-ExpRoute-Code"])
	}
}

func TestCreateBySHA256WithPostUsesRapidShapeAndCapsPartInfos(t *testing.T) {
	const fullHash = "df5e2a9e39493efcc9ab18f571d8a80516652b5295fc5a14c6fe5aba429365cc"
	d := Yun139{}
	dstDir := &model.Object{ID: "parent-id"}
	var gotPath string
	var gotBody map[string]interface{}

	resp, partInfos, err := d.createBySHA256WithPost(
		context.Background(),
		dstDir,
		"movie.mkv",
		101*512*utils.MB,
		fullHash,
		"auto_rename",
		func(path string, data interface{}, resp interface{}) ([]byte, error) {
			gotPath = path
			body, ok := data.(base.Json)
			if !ok {
				t.Fatalf("data type = %T, want base.Json", data)
			}
			gotBody = body
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("createBySHA256WithPost() error = %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}
	if gotPath != "/file/create" {
		t.Fatalf("path = %q, want /file/create", gotPath)
	}
	if len(partInfos) != 101 {
		t.Fatalf("partInfos len = %d, want 101", len(partInfos))
	}
	firstPartInfos, ok := gotBody["partInfos"].([]PartInfo)
	if !ok {
		t.Fatalf("body partInfos type = %T, want []PartInfo", gotBody["partInfos"])
	}
	if len(firstPartInfos) != 100 {
		t.Fatalf("body partInfos len = %d, want 100", len(firstPartInfos))
	}
	if gotBody["contentHash"] != fullHash {
		t.Fatalf("contentHash = %q, want full hash", gotBody["contentHash"])
	}
	if gotBody["contentHashAlgorithm"] != "SHA256" {
		t.Fatalf("contentHashAlgorithm = %q, want SHA256", gotBody["contentHashAlgorithm"])
	}
	if gotBody["parallelUpload"] != false {
		t.Fatalf("parallelUpload = %v, want false", gotBody["parallelUpload"])
	}
	if gotBody["parentFileId"] != "parent-id" {
		t.Fatalf("parentFileId = %q, want parent-id", gotBody["parentFileId"])
	}
}
