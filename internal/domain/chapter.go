package domain

import (
	"fmt"
	"unicode/utf8"
)

// ReviewInterval is the global review interval (triggered every N chapters).
const ReviewInterval = 5

// ShouldReview decides from the completed chapter count whether a global review is due
// (short/mid-length mode).
func ShouldReview(completedCount int) (bool, string) {
	if completedCount > 0 && completedCount%ReviewInterval == 0 {
		return true, fmt.Sprintf("Đã hoàn thành %d chương, kích hoạt thẩm duyệt toàn cục", completedCount)
	}
	return false, ""
}

// ShouldArcReview decides whether an arc-level or volume-level review is due in long-form mode.
func ShouldArcReview(isArcEnd, isVolumeEnd bool, volume, arc int) (bool, string) {
	if isVolumeEnd {
		return true, fmt.Sprintf("tập %d cung %d kết thúc (hết tập), kích hoạt thẩm duyệt cấp cung + cấp tập", volume, arc)
	}
	if isArcEnd {
		return true, fmt.Sprintf("tập %d cung %d kết thúc, kích hoạt thẩm duyệt cấp cung", volume, arc)
	}
	return false, ""
}

// WordCount counts characters by rune.
func WordCount(content string) int {
	return utf8.RuneCountInString(content)
}
