package guangyapan

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	// --- 短信登录（两段式）---
	PhoneNumber    string `json:"phone_number" type:"text" help:"手机号，用于短信登录，例如 13800000000 或 +86 13800000000"`
	CaptchaToken   string `json:"captcha_token" type:"text" help:"风控验证 token，由 /v1/shield/captcha/init 自动获取，一般留空即可"`
	SendCode       bool   `json:"send_code" type:"bool" help:"勾选并保存以发送短信验证码，发送后会自动重置为 false"`
	VerifyCode     string `json:"verify_code" type:"text" help:"短信验证码，配合手机号填写后保存即可完成登录并自动写入令牌"`
	VerificationID string `json:"verification_id" type:"text" help:"发送验证码后自动生成，请勿手动修改"`
	// --- 令牌 ---
	AccessToken  string `json:"access_token" type:"text" required:"false" default:""`
	RefreshToken string `json:"refresh_token" required:"false" type:"text" default:""`
	ClientID     string `json:"client_id" required:"true" default:"aMe-8VSlkrbQXpUR"`
	DeviceID     string `json:"device_id" required:"false" type:"text" default:"" help:"自定义设备 ID（32 位十六进制），留空自动生成"`
	DeviceSign   string `json:"device_sign" required:"false" type:"text" default:"" help:"自定义 X-Device-Sign，留空则由设备 ID 生成"`
	PageSize     int    `json:"page_size" required:"true" type:"number" default:"100"`
	OrderBy      int    `json:"order_by" required:"true" type:"number" default:"3"`
	SortType     int    `json:"sort_type" required:"true" type:"number" default:"1"`
}

var config = driver.Config{
	Name:        "GuangYaPan",
	LocalSort:   true,
	DefaultRoot: "",
	CheckStatus: true,
	Alert:       "info|两段式短信登录：(1) 填手机号（如需要再填 captcha_token），勾选 send_code 并保存以发送验证码；(2) 填写 verify_code 后再保存，即可完成登录并自动保存 access_token / refresh_token。",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &GuangYaPan{}
	})
}
