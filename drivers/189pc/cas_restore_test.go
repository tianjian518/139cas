package _189pc

import (
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestResolveCASRestoreName(t *testing.T) {
	info := &casUploadInfo{Name: "movie.mkv", Size: 1, MD5: "md5", SliceMD5: "slice"}
	tests := []struct {
		name        string
		casName     string
		want        string
		wantErrText string
	}{
		{name: "replace current extension", casName: "abc.mp4.cas", want: "abc.mkv"},
		{name: "append original extension", casName: "test.cas", want: "test.mkv"},
		{name: "upper suffix", casName: "test.CAS", want: "test.mkv"},
		{name: "missing cas suffix", casName: "test.mkv", wantErrText: "does not end with .cas"},
		{name: "empty base", casName: ".cas", wantErrText: "empty source file name"},
		{name: "reject path", casName: "dir/test.cas", wantErrText: "contains a path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCASRestoreName(tt.casName, info)
			if tt.wantErrText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErrText, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAutoRestoreInFlight(t *testing.T) {
	driver := &Cloud189PC{}
	path := "/movies/movie.mkv.cas"
	if !driver.beginAutoRestore(path) {
		t.Fatal("expected first beginAutoRestore to start")
	}
	if driver.beginAutoRestore(path) {
		t.Fatal("expected duplicate beginAutoRestore to be skipped")
	}
	driver.endAutoRestore(path)
	if !driver.beginAutoRestore(path) {
		t.Fatal("expected beginAutoRestore to start after endAutoRestore")
	}
}

func TestManualRefreshAutoRestorePath(t *testing.T) {
	driver := &Cloud189PC{
		Storage: model.Storage{MountPath: "/189pc"},
		Addition: Addition{
			AutoRestoreExistingCAS:      true,
			AutoRestoreExistingCASPaths: "/movies\n/docs",
		},
	}
	tests := []struct {
		name string
		args model.ListArgs
		want string
		ok   bool
	}{
		{name: "monitor root", args: model.ListArgs{ReqPath: "/189pc/movies", Refresh: true}, want: "/movies", ok: true},
		{name: "monitor child", args: model.ListArgs{ReqPath: "/189pc/movies/action", Refresh: true}, want: "/movies/action", ok: true},
		{name: "prefix is not child", args: model.ListArgs{ReqPath: "/189pc/movies-old", Refresh: true}, ok: false},
		{name: "outside monitor", args: model.ListArgs{ReqPath: "/189pc/music", Refresh: true}, ok: false},
		{name: "not refresh", args: model.ListArgs{ReqPath: "/189pc/movies"}, ok: false},
		{name: "missing req path", args: model.ListArgs{Refresh: true}, ok: false},
		{name: "outside mount", args: model.ListArgs{ReqPath: "/other/movies", Refresh: true}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := driver.manualRefreshAutoRestorePath(tt.args)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}
