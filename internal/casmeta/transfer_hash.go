package casmeta

import (
	"encoding/hex"
	"hash"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// TransferHasher 在跨盘搬运时顺路计算源文件的 md5 + sha1。
//
// 夸克的秒传接口 /file/update/hash 同时校验 md5 和 sha1，只传 md5 会被拒绝；
// 而夸克没有任何接口会返回已存在文件的 sha1，所以只能读一遍文件自己算。
// 跨盘搬运本来就必须要读这一遍，把哈希挂在流的旁边即可，不产生额外下载。
//
// 用法：把 *TransferHasher 当 io.Writer 传给 FileStreamer.CacheFullAndWriter。
type TransferHasher struct {
	md5     hash.Hash
	sha1    hash.Hash
	written int64
}

func NewTransferHasher() *TransferHasher {
	return &TransferHasher{
		md5:  utils.MD5.NewFunc(),
		sha1: utils.SHA1.NewFunc(),
	}
}

func (w *TransferHasher) Write(p []byte) (int, error) {
	n := len(p)
	_, _ = w.md5.Write(p)
	_, _ = w.sha1.Write(p)
	w.written += int64(n)
	return n, nil
}

func (w *TransferHasher) Written() int64 { return w.written }

func (w *TransferHasher) MD5() string { return hex.EncodeToString(w.md5.Sum(nil)) }

func (w *TransferHasher) SHA1() string { return hex.EncodeToString(w.sha1.Sum(nil)) }
