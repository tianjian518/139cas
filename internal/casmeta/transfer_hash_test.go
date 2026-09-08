package casmeta

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"testing"
)

func TestTransferHasherMatchesStdlib(t *testing.T) {
	payload := []byte("the quick brown fox jumps over the lazy dog 夸克搬运转 CAS 顺路算哈希\n")
	wantMD5 := md5.Sum(payload)
	wantSHA1 := sha1.Sum(payload)

	// 用不同的分块大小各验证一次，确保累加写入与一次性写入结果一致
	for _, chunk := range []int{1, 7, 64, 4096} {
		h := NewTransferHasher()
		for start := 0; start < len(payload); {
			end := start + chunk
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := h.Write(payload[start:end]); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			start = end
		}
		if got, want := h.Written(), int64(len(payload)); got != want {
			t.Fatalf("chunk=%d Written() = %d, want %d", chunk, got, want)
		}
		if got, want := h.MD5(), hex.EncodeToString(wantMD5[:]); got != want {
			t.Fatalf("chunk=%d MD5() = %q, want %q", chunk, got, want)
		}
		if got, want := h.SHA1(), hex.EncodeToString(wantSHA1[:]); got != want {
			t.Fatalf("chunk=%d SHA1() = %q, want %q", chunk, got, want)
		}
	}
}

func TestTransferHasherMultipleWritesAccumulate(t *testing.T) {
	h := NewTransferHasher()
	for _, p := range [][]byte{[]byte("a"), []byte("b"), []byte("c")} {
		if _, err := h.Write(p); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	want := md5.Sum([]byte("abc"))
	if got := h.MD5(); got != hex.EncodeToString(want[:]) {
		t.Fatalf("MD5() = %q, want %q", got, hex.EncodeToString(want[:]))
	}
	if h.Written() != 3 {
		t.Fatalf("Written() = %d, want 3", h.Written())
	}
}
