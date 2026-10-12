package server

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func decodeWindowsCommandOutput(output []byte) string {
	if utf8.Valid(output) {
		return string(output)
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(output)
	if err == nil {
		return string(decoded)
	}
	return strings.ToValidUTF8(string(output), "�")
}
