package server

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestDecodeWindowsCommandOutputSupportsGBKAndUTF8(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("无法终止子进程"))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeWindowsCommandOutput(gbk); got != "无法终止子进程" {
		t.Fatalf("decoded GBK output = %q", got)
	}
	if got := decodeWindowsCommandOutput([]byte("无法终止子进程")); got != "无法终止子进程" {
		t.Fatalf("UTF-8 output changed: %q", got)
	}
}
