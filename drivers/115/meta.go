package _115

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Cookie               string  `json:"cookie" type:"text" help:"one of QR code token and cookie required"`
	QRCodeToken          string  `json:"qrcode_token" type:"text" help:"one of QR code token and cookie required"`
	QRCodeSource         string  `json:"qrcode_source" type:"select" options:"web,android,ios,tv,alipaymini,wechatmini,qandroid" default:"linux" help:"select the QR code device, default linux"`
	PageSize             int64   `json:"page_size" type:"number" default:"1000" help:"list api per page size of 115 driver"`
	LimitRate            float64 `json:"limit_rate" type:"float" default:"2" help:"limit all api request rate ([limit]r/1s)"`
	GenerateCAS          bool    `json:"generate_cas" help:"After upload, generate a same-name .cas file in the same directory"`
	DeleteSource         bool    `json:"delete_source" help:"After generating the .cas file, delete the uploaded source file"`
	RestoreSourceFromCAS bool    `json:"restore_source_from_cas" help:"Restore source file from .cas metadata"`
	CASExtAllowlist      string  `json:"cas_ext_allowlist" help:"CAS extension allowlist. Empty means all extensions are allowed. Example: mp4,mkv,iso,zip"`
	CASDownloadRestore   bool    `json:"cas_download_restore" help:"When enabled, downloading .cas files via /d/* will restore and return the real file instead of raw CAS metadata"`
	driver.RootID
}

var config = driver.Config{
	Name:          "115 Cloud",
	DefaultRoot:   "0",
	LinkCacheMode: driver.LinkCacheUA,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Pan115{}
	})
}
