package doubao_new

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// 豆包/飞书 DPoP 自动续期所需的编译期常量。
// 这些值由飞书 Web 前端硬编码，全站统一，正常情况下无需用户填写。
// 留出 Addition 字段只是为了在服务端策略变更时能紧急覆盖。
const (
	DefaultDPoPKeySecret  = "passport-dpop-token-generator"
	DefaultAuthClientID   = "cli_a872ee858eae100e"
	DefaultAuthClientType = "Lark"
	DefaultAuthScope      = "internal"
	DefaultAuthSDKSource  = "web"
	DefaultAuthSDKVersion = "2.1.10"
	DefaultAppID          = "497858"
)

type Addition struct {
	// Usually one of two
	driver.RootID
	// define other
	Cookie        string `json:"cookie" required:"true" help:"Web Cookie（必填：从浏览器复制豆包网页的完整 Cookie，需包含 feishu_dpop_keypair 与 passport_csrf_token）"`
	AppID         string `json:"app_id" default:"497858" help:"Doubao App ID（一般无需修改）"`
	DPoPKeySecret string `json:"dpop_key_secret" default:"passport-dpop-token-generator" help:"DPoP 密钥解密口令（一般无需修改，默认 passport-dpop-token-generator）"`
	AuthClientID  string `json:"auth_client_id" default:"cli_a872ee858eae100e" help:"Biz Auth Client ID（一般无需修改）"`
	// 以下四项保留占位，正常情况下由驱动自动填充默认值
	AuthClientType string `json:"auth_client_type" default:"Lark" help:"Biz Auth Client Type（一般无需修改）"`
	AuthScope      string `json:"auth_scope" default:"internal" help:"Biz Auth Scope（一般无需修改）"`
	AuthSDKSource  string `json:"auth_sdk_source" default:"web" help:"Biz Auth SDK Source（一般无需修改）"`
	AuthSDKVersion string `json:"auth_sdk_version" default:"2.1.10" help:"Biz Auth SDK Version（一般无需修改）"`
	ShareLink      bool   `json:"share_link" help:"Whether to use share link for download"`
	IgnoreJWTCheck bool   `json:"ignore_jwt_check" help:"Whether to ignore JWT check to prevent time issue"`
}

var config = driver.Config{
	Name:        "DoubaoNew",
	LocalSort:   true,
	DefaultRoot: "",
	Alert: `danger|Do not use 302 if the storage is public accessible.
Otherwise, the download link may leak sensitive information such as access token or signature.
Others may use the leaked link to access all your files.

提示：只需填写 Cookie 即可自动续期。若续期失败，请检查 Cookie 中是否包含
feishu_dpop_keypair 与 passport_csrf_token 这两项。`,
	NoOverwriteUpload: false,
	PreferProxy:       true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &DoubaoNew{}
	})
}
