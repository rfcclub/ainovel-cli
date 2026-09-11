package utils

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// DecodeText decodes user-supplied text-file bytes into UTF-8: when the bytes are not valid
// UTF-8 it transcodes via GB18030 (a superset of GBK) — the Chinese novels circulating
// online are largely GBK-encoded, and reading them as UTF-8 yields pure mojibake. Byte
// sequences that are not GBK get replaced with U+FFFD by the decoder (they were mojibake to
// begin with, and the caller's zero-hit fallback reports it and guides the user). Finally a
// UTF-8 BOM is stripped (otherwise line-start matching would carry it).
func DecodeText(data []byte) string {
	if !utf8.Valid(data) {
		if decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data); err == nil {
			data = decoded
		}
	}
	return strings.TrimPrefix(string(data), "\uFEFF")
}
