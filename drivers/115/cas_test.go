package _115

import (
	"io"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestValidate115CASInfoRequiresProviderSHA1AndPreID(t *testing.T) {
	driver := &Pan115{}

	tests := []struct {
		name    string
		info    *casUploadInfo
		wantErr string
	}{
		{
			name: "valid 115 cas",
			info: &casUploadInfo{
				Provider: casProvider115,
				Name:     "movie.mkv",
				Size:     1024,
				SHA1:     strings.Repeat("A", 40),
				PreID:    strings.Repeat("B", 40),
			},
		},
		{
			name: "reject foreign provider",
			info: &casUploadInfo{
				Provider: "139",
				Name:     "movie.mkv",
				Size:     1024,
				SHA1:     strings.Repeat("A", 40),
				PreID:    strings.Repeat("B", 40),
			},
			wantErr: "provider",
		},
		{
			name: "reject missing sha1",
			info: &casUploadInfo{
				Provider: casProvider115,
				Name:     "movie.mkv",
				Size:     1024,
				PreID:    strings.Repeat("B", 40),
			},
			wantErr: "sha1",
		},
		{
			name: "reject missing preID",
			info: &casUploadInfo{
				Provider: casProvider115,
				Name:     "movie.mkv",
				Size:     1024,
				SHA1:     strings.Repeat("A", 40),
			},
			wantErr: "preID",
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

func TestPan115CASPreviewNameHonorsAllowlist(t *testing.T) {
	driver := &Pan115{Addition: Addition{CASExtAllowlist: "mp4"}}
	info := &casmeta.Info{Name: "movie.mkv"}

	got, err := resolveCASRestoreName("movie.mkv.cas", info)
	if err != nil {
		t.Fatalf("resolveCASRestoreName() error = %v", err)
	}
	if casmeta.ExtAllowed(got, driver.CASExtAllowlist) {
		t.Fatalf("ExtAllowed(%q, %q) = true, want false", got, driver.CASExtAllowlist)
	}
}

func TestPan115CASFeatureSwitches(t *testing.T) {
	driver := &Pan115{Addition: Addition{
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

func TestPan115ShouldPlayCASOnlyForCASVideoType(t *testing.T) {
	driver := &Pan115{}
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

func TestCASRestoreStreamCannotSatisfySignCheckRanges(t *testing.T) {
	stream := &casRestoreStream{
		name: "movie.mkv",
		info: &casUploadInfo{
			Name:  "movie.mkv",
			Size:  1024,
			SHA1:  strings.Repeat("A", 40),
			PreID: strings.Repeat("B", 40),
		},
	}

	if _, err := stream.RangeRead(http_range.Range{Start: 10, Length: 20}); err == nil || !strings.Contains(err.Error(), "source bytes") {
		t.Fatalf("RangeRead() error = %v, want source bytes error", err)
	}
	if _, err := stream.CacheFullAndWriter(nil, io.Discard); err == nil || !strings.Contains(err.Error(), "no source bytes") {
		t.Fatalf("CacheFullAndWriter() error = %v, want no source bytes error", err)
	}
	if got := stream.GetHash().GetHash(utils.SHA1); got != strings.Repeat("A", 40) {
		t.Fatalf("GetHash(SHA1) = %q", got)
	}

	var _ model.FileStreamer = stream
}
