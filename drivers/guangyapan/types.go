package guangyapan

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Object struct {
	model.Object
	ParentID string
	DriveID  string
}

type apiResponse map[string]any

func (o *Object) ModTime() time.Time {
	return o.Object.ModTime()
}

var _ model.Obj = (*Object)(nil)

// --- 账号 / 登录相关响应 ---

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Sub          string `json:"sub"`
	Error        string `json:"error"`
	ErrorCode    int    `json:"error_code"`
	ErrorDesc    string `json:"error_description"`
	Msg          string `json:"msg"`
}

type verificationResp struct {
	VerificationID string `json:"verification_id"`
	Error          string `json:"error"`
	ErrorCode      int    `json:"error_code"`
	ErrorDesc      string `json:"error_description"`
}

type captchaInitResp struct {
	CaptchaToken string `json:"captcha_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorCode    int    `json:"error_code"`
	ErrorDesc    string `json:"error_description"`
}

type verifyResp struct {
	VerificationToken string `json:"verification_token"`
	Error             string `json:"error"`
	ErrorCode         int    `json:"error_code"`
	ErrorDesc         string `json:"error_description"`
}

type userMeResp struct {
	Sub string `json:"sub"`
}

func unixOrZero(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}
