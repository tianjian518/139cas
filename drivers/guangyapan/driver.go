package guangyapan

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type GuangYaPan struct {
	model.Storage
	Addition
	client        *resty.Client
	accountClient *resty.Client
}

// persistAddition 把 Addition 变更落库。测试环境下无数据库，可在用例里替换为空实现。
var persistAddition = func(d *GuangYaPan) {
	op.MustSaveDriverStorage(d)
}

func (d *GuangYaPan) Config() driver.Config {
	return config
}

func (d *GuangYaPan) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *GuangYaPan) Init(ctx context.Context) error {
	d.initClient()

	// 登录优先级：access_token → refresh_token → 短信验证码。
	if d.AccessToken != "" {
		if err := d.validateToken(ctx); err == nil {
			return nil
		}
		d.AccessToken = ""
	}
	if d.RefreshToken != "" {
		if err := d.refreshAccessToken(ctx); err == nil {
			if err := d.validateToken(ctx); err == nil {
				return nil
			}
		}
	}

	// 两段式短信登录：
	// 1) 只填手机号 + send_code=true：发送验证码并缓存 verification_id（不中断初始化）
	// 2) 手机号 + verify_code：完成登录并保存令牌
	if d.PhoneNumber != "" {
		if d.canSMSLogin() {
			if err := d.loginBySMSCode(ctx); err != nil {
				return err
			}
			return d.validateToken(ctx)
		}
		if d.SendCode {
			if err := d.prepareSMSCode(ctx); err != nil {
				log.Warnf("guangyapan: 发送短信验证码失败: %v", err)
				return fmt.Errorf("发送短信验证码失败：%v（可在保存后重试，或手动填写 access_token）", err)
			}
			log.Info("guangyapan: 短信验证码已发送，请填写 verify_code 后保存以完成登录")
		}
		return nil
	}

	if d.AccessToken == "" {
		return errors.New("光鸭云盘未登录：请填写 access_token、refresh_token，或使用手机号+验证码短信登录")
	}
	return nil
}

func (d *GuangYaPan) Drop(ctx context.Context) error {
	return nil
}

func (d *GuangYaPan) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.listAll(ctx, normalizeFileID(dir.GetID()))
}

func (d *GuangYaPan) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	return d.createDir(ctx, normalizeFileID(parentDir.GetID()), dirName)
}

func (d *GuangYaPan) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file.IsDir() {
		return nil, errors.New("cannot get link for directory")
	}
	url, err := d.downloadURL(ctx, file)
	if err != nil {
		return nil, err
	}
	expiration := time.Hour
	return &model.Link{
		URL:        url,
		Expiration: &expiration,
		Header: http.Header{
			"User-Agent": {base.UserAgent},
		},
	}, nil
}

func (d *GuangYaPan) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcID := normalizeFileID(srcObj.GetID())
	if srcID == "" {
		return errors.New("file_id is empty")
	}
	return d.moveFile(ctx, srcID, normalizeFileID(dstDir.GetID()))
}

func (d *GuangYaPan) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcID := normalizeFileID(srcObj.GetID())
	if srcID == "" {
		return errors.New("file_id is empty")
	}
	return d.copyFile(ctx, srcID, normalizeFileID(dstDir.GetID()))
}

func (d *GuangYaPan) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	fileID := normalizeFileID(srcObj.GetID())
	if fileID == "" {
		return errors.New("file_id is empty")
	}
	if newName == "" {
		return errors.New("new_name is empty")
	}
	return d.renameFile(ctx, fileID, newName)
}

func (d *GuangYaPan) Remove(ctx context.Context, obj model.Obj) error {
	fileID := normalizeFileID(obj.GetID())
	if fileID == "" {
		return errors.New("file_id is empty")
	}
	return d.deleteFile(ctx, fileID)
}

func (d *GuangYaPan) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	var resp apiResponse
	err := d.request(ctx, http.MethodPost, apiBaseURL+"/nd.bizassets.s/v1/get_assets", map[string]any{}, &resp, true)
	if err != nil {
		return nil, err
	}
	if !isSuccess(resp) {
		return nil, responseError(resp)
	}
	data := dataMap(resp)
	total := int64(intFromAny(findValue(data, "totalSpaceSize", "total_space", "totalSpace", "total", "totalSize", "capacity", "quotaSize")))
	used := int64(intFromAny(findValue(data, "usedSpaceSize", "used_space", "usedSpace", "used", "usedSize", "spaceUsed", "fileSize")))
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  used,
		},
	}, nil
}

func normalizeFileID(fileID string) string {
	if fileID == "root" {
		return ""
	}
	return fileID
}

var (
	_ driver.Driver      = (*GuangYaPan)(nil)
	_ driver.Mkdir       = (*GuangYaPan)(nil)
	_ driver.Move        = (*GuangYaPan)(nil)
	_ driver.Copy        = (*GuangYaPan)(nil)
	_ driver.Rename      = (*GuangYaPan)(nil)
	_ driver.Remove      = (*GuangYaPan)(nil)
	_ driver.PutResult   = (*GuangYaPan)(nil)
	_ driver.WithDetails = (*GuangYaPan)(nil)
)
