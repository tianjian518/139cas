package quark

import (
	"io"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestQuarkValidateCASInfoRequiresMD5AndSHA1(t *testing.T) {
	driver := &QuarkOrUC{}
	validMD5 := strings.Repeat("a", 32)
	validSHA1 := strings.Repeat("b", 40)

	tests := []struct {
		name    string
		info    *casUploadInfo
		wantErr string
	}{
		{
			name: "valid quark cas",
			info: &casUploadInfo{
				Provider: casProviderQuark,
				Name:     "movie.mkv",
				Size:     1024,
				MD5:      validMD5,
				SHA1:     validSHA1,
			},
		},
		{
			name: "provider is optional",
			info: &casUploadInfo{
				Name: "movie.mkv",
				Size: 1024,
				MD5:  validMD5,
				SHA1: validSHA1,
			},
		},
		{
			name: "reject foreign provider",
			info: &casUploadInfo{
				Provider: "115",
				Name:     "movie.mkv",
				Size:     1024,
				MD5:      validMD5,
				SHA1:     validSHA1,
			},
			wantErr: "provider",
		},
		{
			name: "reject missing md5",
			info: &casUploadInfo{
				Provider: casProviderQuark,
				Name:     "movie.mkv",
				Size:     1024,
				SHA1:     validSHA1,
			},
			wantErr: "md5",
		},
		{
			name: "reject missing sha1",
			info: &casUploadInfo{
				Provider: casProviderQuark,
				Name:     "movie.mkv",
				Size:     1024,
				MD5:      validMD5,
			},
			wantErr: "sha1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := driver.validateCASInfo(tt.info)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateCASInfo() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateCASInfo() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestQuarkCASPayloadRoundTripKeepsMD5AndSHA1(t *testing.T) {
	info := &casUploadInfo{
		Provider: casProviderQuark,
		Name:     "movie.mkv",
		Size:     2048,
		MD5:      strings.Repeat("a", 32),
		SHA1:     strings.Repeat("b", 40),
	}
	content, err := casmeta.Encode(info)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := (&QuarkOrUC{}).parseCAS(content)
	if err != nil {
		t.Fatalf("parseCAS() error = %v", err)
	}
	if decoded.MD5 != info.MD5 || decoded.SHA1 != info.SHA1 || decoded.Size != info.Size {
		t.Fatalf("round trip mismatch: %+v", decoded)
	}
	if err = (&QuarkOrUC{}).validateCASInfo(decoded); err != nil {
		t.Fatalf("validateCASInfo() error = %v", err)
	}
}

func TestQuarkCASFeatureSwitches(t *testing.T) {
	driver := &QuarkOrUC{Addition: Addition{
		GenerateCAS:        true,
		DeleteSource:       true,
		CASExtAllowlist:    "mkv",
		CASDownloadRestore: true,
	}}

	if !driver.shouldUploadCAS("movie.mkv") {
		t.Fatal("shouldUploadCAS(movie.mkv) = false, want true")
	}
	if driver.shouldUploadCAS("movie.mkv.cas") {
		t.Fatal("shouldUploadCAS(movie.mkv.cas) = true, want false")
	}
	if driver.shouldUploadCAS("movie.mp4") {
		t.Fatal("shouldUploadCAS(movie.mp4) = true, want false")
	}
	if !driver.shouldDeleteSource() {
		t.Fatal("shouldDeleteSource() = false, want true")
	}
	if !driver.CASDownloadRestoreEnabled() {
		t.Fatal("CASDownloadRestoreEnabled() = false, want true")
	}
}

func TestQuarkShouldPlayCASOnlyForCASVideoType(t *testing.T) {
	driver := &QuarkOrUC{}
	casObj := &model.Object{Name: "movie.mkv.cas"}
	rawObj := &model.Object{Name: "movie.mkv"}

	if !driver.shouldPlayCAS(casObj, model.LinkArgs{Type: "cas_video"}) {
		t.Fatal("shouldPlayCAS(.cas, cas_video) = false, want true")
	}
	if driver.shouldPlayCAS(casObj, model.LinkArgs{Type: "raw_cas"}) {
		t.Fatal("shouldPlayCAS(.cas, raw_cas) = true, want false")
	}
	if driver.shouldPlayCAS(casObj, model.LinkArgs{Type: "raw_file"}) {
		t.Fatal("shouldPlayCAS(.cas, raw_file) = true, want false")
	}
	if driver.shouldPlayCAS(rawObj, model.LinkArgs{Type: "cas_video"}) {
		t.Fatal("shouldPlayCAS(raw, cas_video) = true, want false")
	}
}

func TestQuarkCASRestoreNameHonorsAllowlist(t *testing.T) {
	driver := &QuarkOrUC{Addition: Addition{CASExtAllowlist: "mp4"}}
	info := &casmeta.Info{Name: "movie.mkv"}

	got, err := casmeta.ResolveRestoreName("movie.mkv.cas", info)
	if err != nil {
		t.Fatalf("ResolveRestoreName() error = %v", err)
	}
	if got != "movie.mkv" {
		t.Fatalf("ResolveRestoreName() = %q, want movie.mkv", got)
	}
	if casmeta.ExtAllowed(got, driver.CASExtAllowlist) {
		t.Fatalf("ExtAllowed(%q, %q) = true, want false", got, driver.CASExtAllowlist)
	}
}

func TestQuarkCASRestoreStreamCannotSatisfyRanges(t *testing.T) {
	stream := &casRestoreStream{
		name: "movie.mkv",
		info: &casUploadInfo{
			Name: "movie.mkv",
			Size: 1024,
			MD5:  strings.Repeat("a", 32),
			SHA1: strings.Repeat("b", 40),
		},
	}

	if _, err := stream.RangeRead(http_range.Range{Start: 10, Length: 20}); err == nil || !strings.Contains(err.Error(), "source bytes") {
		t.Fatalf("RangeRead() error = %v, want source bytes error", err)
	}
	if _, err := stream.CacheFullAndWriter(nil, io.Discard); err == nil || !strings.Contains(err.Error(), "no source bytes") {
		t.Fatalf("CacheFullAndWriter() error = %v, want no source bytes error", err)
	}
	if got := stream.GetHash().GetHash(utils.SHA1); got != strings.Repeat("b", 40) {
		t.Fatalf("GetHash(SHA1) = %q", got)
	}
	if got := stream.GetHash().GetHash(utils.MD5); got != strings.Repeat("a", 32) {
		t.Fatalf("GetHash(MD5) = %q", got)
	}

	var _ model.FileStreamer = stream
}
