package guangyapan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/go-resty/resty/v2"
)

// 本文件实现光鸭云盘的短信验证码登录（两段式），逻辑与官方 OpenList 驱动保持一致：
//   1) 填手机号 + 勾选 send_code 并保存 → 走风控 init 拿 captcha_token → 请求验证码，落 verification_id
//   2) 填 verify_code 再保存 → verify + signin，拿 access_token / refresh_token 并落库

func normalizeDeviceID(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "")
	if len(v) != 32 {
		return ""
	}
	for _, ch := range v {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
	}
	return v
}

func randomDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0123456789abcdef0123456789abcdef"
	}
	return hex.EncodeToString(b)
}

// normalizeCaptchaUsername 只保留数字，并去掉中国大陆号码的 86 前缀。
func normalizeCaptchaUsername(phone string) string {
	p := strings.TrimSpace(phone)
	p = strings.ReplaceAll(p, " ", "")
	p = strings.TrimPrefix(p, "+")
	b := make([]rune, 0, len(p))
	for _, ch := range p {
		if ch >= '0' && ch <= '9' {
			b = append(b, ch)
		}
	}
	digits := string(b)
	if strings.HasPrefix(digits, "86") && len(digits) > 11 {
		digits = digits[2:]
	}
	return digits
}

// normalizePhoneE164 归一化为浏览器实际发送的 "+86 1xxxxxxxxxx" 形式。
func normalizePhoneE164(phone string) string {
	p := strings.TrimSpace(phone)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, " ", "")
	if strings.HasPrefix(p, "+") {
		if strings.HasPrefix(p, "+86") && len(p) > 3 {
			return "+86 " + strings.TrimPrefix(p, "+86")
		}
		return p
	}
	digits := normalizeCaptchaUsername(p)
	if len(digits) == 11 {
		return "+86 " + digits
	}
	return p
}

func (d *GuangYaPan) accountErr(desc, short string, body string) string {
	msg := strings.TrimSpace(desc)
	if msg == "" {
		msg = strings.TrimSpace(short)
	}
	if msg == "" {
		msg = strings.TrimSpace(body)
	}
	if msg == "" {
		msg = "unknown error"
	}
	return msg
}

func (d *GuangYaPan) canSMSLogin() bool {
	return d.PhoneNumber != "" && d.VerifyCode != ""
}

// accountRequest 保证 accountClient 已初始化（DRY：登录相关方法统一入口）。
func (d *GuangYaPan) accountRequest() *resty.Request {
	if d.accountClient == nil {
		d.initClient()
	}
	return d.accountClient.R()
}

// ensureAccountClient 在客户端未初始化时补一次（供 validateToken 等直接使用）。
func (d *GuangYaPan) ensureAccountClient() {
	if d.accountClient == nil {
		d.initClient()
	}
}

// loginBySMSCode 第二段：校验验证码并换取令牌。
func (d *GuangYaPan) loginBySMSCode(ctx context.Context) error {
	verificationID := strings.TrimSpace(d.VerificationID)
	if verificationID == "" {
		var err error
		verificationID, err = d.requestVerificationID(ctx)
		if err != nil {
			return err
		}
	}

	var step2 verifyResp
	resp, err := d.accountRequest().
		SetContext(ctx).
		SetBody(map[string]any{
			"verification_id":   verificationID,
			"verification_code": d.VerifyCode,
			"client_id":         d.ClientID,
		}).
		SetResult(&step2).
		Post("/v1/auth/verification/verify")
	if err != nil {
		return err
	}
	if resp.IsError() || step2.Error != "" || strings.TrimSpace(step2.VerificationToken) == "" {
		return fmt.Errorf("verify code failed: %s", d.accountErr(step2.ErrorDesc, step2.Error, resp.String()))
	}

	var out tokenResp
	resp, err = d.accountRequest().
		SetContext(ctx).
		SetBody(map[string]any{
			"verification_code":  d.VerifyCode,
			"verification_token": step2.VerificationToken,
			"username":           normalizePhoneE164(d.PhoneNumber),
			"client_id":          d.ClientID,
		}).
		SetResult(&out).
		Post("/v1/auth/signin")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		return fmt.Errorf("signin failed: %s", d.accountErr(out.ErrorDesc, out.Error, resp.String()))
	}

	d.AccessToken = strings.TrimSpace(out.AccessToken)
	d.RefreshToken = strings.TrimSpace(out.RefreshToken)
	d.VerificationID = ""
	// 短信验证码一次性，登录成功后立刻清空，避免复用。
	d.VerifyCode = ""
	persistAddition(d)
	return nil
}

// prepareSMSCode 第一段：请求发送短信验证码，并把 verification_id 落库。
func (d *GuangYaPan) prepareSMSCode(ctx context.Context) error {
	// 每次显式发送都重新取 verification_id。
	d.VerificationID = ""
	if err := d.ensureCaptchaToken(ctx, false); err != nil {
		return err
	}
	verificationID, err := d.requestVerificationID(ctx)
	if err != nil {
		return err
	}
	d.VerificationID = verificationID
	d.SendCode = false
	persistAddition(d)
	return nil
}

func (d *GuangYaPan) requestVerificationID(ctx context.Context) (string, error) {
	req := d.accountRequest().SetContext(ctx)
	if d.CaptchaToken != "" {
		req.SetHeader("X-Captcha-Token", d.CaptchaToken)
	}

	var step1 verificationResp
	resp, err := req.
		SetBody(map[string]any{
			"phone_number": normalizePhoneE164(d.PhoneNumber),
			"target":       "ANY",
			"client_id":    d.ClientID,
		}).
		SetResult(&step1).
		Post("/v1/auth/verification")
	if err != nil {
		return "", err
	}
	if resp.IsError() || step1.Error != "" || strings.TrimSpace(step1.VerificationID) == "" {
		// captcha token 过期/失效时刷新一次再重试。
		if strings.Contains(step1.Error, "captcha_invalid") || strings.Contains(step1.ErrorDesc, "captcha_token expired") {
			if err := d.ensureCaptchaToken(ctx, true); err == nil {
				return d.requestVerificationID(ctx)
			}
		}
		return "", fmt.Errorf("request verification failed: %s", d.accountErr(step1.ErrorDesc, step1.Error, resp.String()))
	}
	return strings.TrimSpace(step1.VerificationID), nil
}

func (d *GuangYaPan) ensureCaptchaToken(ctx context.Context, force bool) error {
	if !force && d.CaptchaToken != "" {
		return nil
	}

	var out captchaInitResp
	req := d.accountRequest().SetContext(ctx)
	if d.CaptchaToken != "" {
		req.SetHeader("X-Captcha-Token", d.CaptchaToken)
	}
	resp, err := req.
		SetBody(map[string]any{
			"client_id": d.ClientID,
			"action":    "POST:/v1/auth/verification",
			"device_id": d.DeviceID,
			"meta": map[string]any{
				"username":           normalizePhoneE164(d.PhoneNumber),
				"phone_number":       normalizePhoneE164(d.PhoneNumber),
				"VERIFICATION_PHONE": normalizePhoneE164(d.PhoneNumber),
			},
		}).
		SetResult(&out).
		Post("/v1/shield/captcha/init")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.CaptchaToken) == "" {
		return fmt.Errorf("init captcha token failed: %s", d.accountErr(out.ErrorDesc, out.Error, resp.String()))
	}
	d.CaptchaToken = strings.TrimSpace(out.CaptchaToken)
	persistAddition(d)
	return nil
}

// validateToken 用于校验 access_token 是否可用。
func (d *GuangYaPan) validateToken(ctx context.Context) error {
	d.ensureAccountClient()
	var me userMeResp
	resp, err := d.accountRequest().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetResult(&me).
		Get("/v1/user/me")
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("validate token failed: status=%d body=%s", resp.StatusCode(), resp.String())
	}
	if strings.TrimSpace(me.Sub) == "" {
		return errors.New("validate token failed: empty user sub")
	}
	return nil
}
