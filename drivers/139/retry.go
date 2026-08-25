package _139

import (
	"net/http"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

const (
	// 139（移动云/和彩云）接口重试参数
	yun139MaxRetry      = 5                      // 最多 5 次重试（合计 6 次尝试）
	yun139RetryWaitBase = 800 * time.Millisecond // 首次重试等待
	yun139RetryWaitMax  = 10 * time.Second       // 退避上限
)

// is139RetryCondition 决定 139 接口响应是否需要重试。
//
// 覆盖以下场景：
//   - 网络层临时错误（err != nil，如连接重置/超时）
//   - HTTP 429 限流
//   - 5xx 服务端错误
//   - 业务层 success=false，但 message 透出了限流特征
//     （典型的就是移动盘接口被限流时返回的 "状态码非 200"，
//      以及 "频繁"/"限流"/"too many"/"rate limit"/"busy"/"稍后" 等）
//
// 这正是「夸克→移动盘」复制时返回 429 + "状态码非 200" 的根因：
// 移动盘(139)管理接口（/file/create、/file/getUploadUrl、/file/complete 等）
// 被限流，而原代码没有任何重试，直接把报文 message 抛给了上层。
func is139RetryCondition(resp *resty.Response, err error) bool {
	if err != nil {
		// 网络层临时错误（连接重置/超时/DNS 抖动等）一律重试
		return true
	}
	if resp == nil {
		return false
	}
	if resp.StatusCode() == http.StatusTooManyRequests || resp.StatusCode() >= 500 {
		return true
	}
	// 业务层：success=false 且 message 含限流特征时重试
	var e BaseResp
	if jsonErr := utils.Json.Unmarshal(resp.Body(), &e); jsonErr == nil && !e.Success {
		msg := strings.ToLower(e.Message)
		if strings.Contains(msg, "状态码非 200") ||
			strings.Contains(msg, "频繁") ||
			strings.Contains(msg, "限流") ||
			strings.Contains(msg, "too many") ||
			strings.Contains(msg, "rate limit") ||
			strings.Contains(msg, "busy") ||
			strings.Contains(msg, "try again") ||
			strings.Contains(msg, "稍后") {
			return true
		}
	}
	return false
}

// do139Execute 执行 139 接口请求，并对「429/5xx/网络抖动/限流文案」自动重试。
//
// 用法：把原来的 `req.Execute(method, url)` / `.Post(url)` 替换为 `do139Execute(req, method, url)` 即可。
// 最多 6 次尝试（1 次首发 + 5 次重试），800ms→10s 指数退避。
func do139Execute(req *resty.Request, method, url string) (*resty.Response, error) {
	var res *resty.Response
	var err error
	for attempt := 0; attempt <= yun139MaxRetry; attempt++ {
		if attempt > 0 {
			wait := yun139RetryWaitBase * time.Duration(1<<uint(attempt-1))
			if wait > yun139RetryWaitMax {
				wait = yun139RetryWaitMax
			}
			log.Warnf("[139] 接口临时失败，%s 后第 %d/%d 次重试: %s %s (err=%v)",
				wait, attempt, yun139MaxRetry, method, url, err)
			time.Sleep(wait)
		}
		res, err = req.Execute(method, url)
		if !is139RetryCondition(res, err) {
			return res, err
		}
	}
	return res, err
}
