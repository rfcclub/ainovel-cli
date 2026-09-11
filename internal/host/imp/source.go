package imp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Supported source encoding labels, written into the Manifest and progress events with no silent fallback (RFC §7.1).
const (
	encodingUTF8    = "utf-8"
	encodingUTF8BOM = "utf-8-bom"
	encodingGB18030 = "gb18030"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// decoded is one decoding result: the text plus the encoding actually chosen.
type decoded struct {
	text     string
	encoding string
}

// decodeSource decodes in UTF-8 / UTF-8 BOM / GB18030 order and returns the encoding chosen.
// It fails outright when decoding is unreliable or replacement characters appear, with the error carrying the
// detection result — "trying GB18030" is never hidden as a silent fallback.
func decodeSource(raw []byte) (decoded, error) {
	if bytes.HasPrefix(raw, utf8BOM) {
		body := raw[len(utf8BOM):]
		if !utf8.Valid(body) {
			return decoded{}, fmt.Errorf("có khai báo UTF-8 BOM nhưng nội dung không phải UTF-8 hợp lệ")
		}
		return decoded{text: string(body), encoding: encodingUTF8BOM}, nil
	}
	if utf8.Valid(raw) {
		return decoded{text: string(raw), encoding: encodingUTF8}, nil
	}
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return decoded{}, fmt.Errorf("không phải UTF-8 hợp lệ và giải mã GB18030 cũng thất bại: %w", err)
	}
	if !utf8.Valid(out) {
		return decoded{}, fmt.Errorf("kết quả giải mã GB18030 vẫn không phải UTF-8 hợp lệ, không thể giải mã đáng tin cậy")
	}
	if i := bytes.IndexRune(out, utf8.RuneError); i >= 0 {
		return decoded{}, fmt.Errorf("giải mã GB18030 xuất hiện ký tự thay thế (U+FFFD @ byte %d), không thể giải mã đáng tin cậy; hãy xác nhận mã hóa của file", i)
	}
	return decoded{text: string(out), encoding: encodingGB18030}, nil
}

// normalize performs only conversions that do not change literary content: CRLF/CR unified to LF.
// Blank lines, indentation, title lines and prose characters are preserved; opening text, empty chapters,
// advertisements and so-called tail noise are not deleted (RFC §7.2).
// The BOM was already stripped during decodeSource.
func normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

// Ingest reads the source file, decodes and normalises it, and atomically creates the meta/import/ workspace
// snapshot by renaming a directory.
// It returns the workspace handle and the Manifest, from which the caller emits progress events.
func Ingest(bookDir, sourcePath string, in Intent) (*Workspace, *Manifest, error) {
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, nil, fmt.Errorf("đọc file nguồn: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil, fmt.Errorf("file nguồn rỗng: %s", sourcePath)
	}
	dec, err := decodeSource(raw)
	if err != nil {
		return nil, nil, err
	}
	normBytes := []byte(normalize(dec.text))

	m := Manifest{
		Version:          workspaceSchemaVersion,
		SourceName:       filepath.Base(sourcePath),
		RawSHA256:        Digest(raw),
		NormalizedSHA256: Digest(normBytes),
		Encoding:         dec.encoding,
		SizeBytes:        int64(len(raw)),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	if in.Version == 0 {
		in.Version = workspaceSchemaVersion
	}

	ws, err := createWorkspace(bookDir, m, in, normBytes)
	if err != nil {
		return nil, nil, err
	}
	return ws, &m, nil
}

// SourceUnit is the stable coordinate the model can reference (RFC §7.3).
// The ID serves display and model reference only; every ordering/containment/increment check uses numeric
// (Line, Part) order, and lexicographic comparison of ID strings is forbidden.
type SourceUnit struct {
	ID        string `json:"id"`   // L1257; an over-budget line is split into L1257.1, L1257.2
	Line      int    `json:"line"` // 1-based
	Part      int    `json:"part"` // 0 = whole line; virtual slice 1..N
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	Text      string `json:"text"`
}

// unitLess defines SourceUnit's total order: Line then Part, both compared numerically (A1 revision).
func unitLess(a, b SourceUnit) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Part < b.Part
}

// buildSourceUnits builds the stable coordinate table from the normalised text.
// An ordinary line is one unit; when a single line exceeds maxUnitBytes, several virtual units are generated at
// UTF-8 character boundaries only, with nothing written back to source.txt, no soft wrapping inserted and no source
// character changed (RFC §7.3). maxUnitBytes<=0 means no fragmentation.
func buildSourceUnits(normalized []byte, maxUnitBytes int) []SourceUnit {
	var units []SourceUnit
	n := len(normalized)
	line := 0
	offset := 0
	for offset < n {
		nl := bytes.IndexByte(normalized[offset:], '\n')
		lineEnd := n
		if nl >= 0 {
			lineEnd = offset + nl
		}
		line++
		if maxUnitBytes > 0 && lineEnd-offset > maxUnitBytes {
			part := 0
			s := offset
			for s < lineEnd {
				e := s + maxUnitBytes
				if e >= lineEnd {
					e = lineEnd
				} else {
					for e > s && !utf8.RuneStart(normalized[e]) {
						e--
					}
					if e == s { // Extreme fallback for a single over-long rune.
						e = s + maxUnitBytes
					}
				}
				part++
				units = append(units, SourceUnit{
					ID: fmt.Sprintf("L%d.%d", line, part), Line: line, Part: part,
					StartByte: s, EndByte: e, Text: string(normalized[s:e]),
				})
				s = e
			}
		} else {
			units = append(units, SourceUnit{
				ID: fmt.Sprintf("L%d", line), Line: line, Part: 0,
				StartByte: offset, EndByte: lineEnd, Text: string(normalized[offset:lineEnd]),
			})
		}
		if nl < 0 {
			break
		}
		offset = lineEnd + 1
	}
	return units
}

// resolveBoundaryByte maps one boundary decision onto an exact byte position: with no anchor it takes the unit's
// start, while with an anchor it requires a unique verbatim hit inside that unit, then maps it to a byte offset
// (RFC §8.3).
func resolveBoundaryByte(unitByID map[string]SourceUnit, unitID, anchor string) (int, error) {
	u, ok := unitByID[unitID]
	if !ok {
		return 0, fmt.Errorf("ranh giới tham chiếu unit không tồn tại: %s", unitID)
	}
	if anchor == "" {
		return u.StartByte, nil
	}
	switch strings.Count(u.Text, anchor) {
	case 0:
		return 0, fmt.Errorf("điểm neo %q không nằm trong unit %s", anchor, unitID)
	case 1:
		return u.StartByte + strings.Index(u.Text, anchor), nil
	default:
		return 0, fmt.Errorf("điểm neo %q không duy nhất trong unit %s", anchor, unitID)
	}
}
