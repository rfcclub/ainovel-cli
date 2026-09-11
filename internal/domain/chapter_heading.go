package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// The chapter number may be written in one of three languages: Vietnamese "Chương 12",
	// Chinese "第 12 章" or English "Chapter 12".
	chapterNumberRe = regexp.MustCompile(`^\s*(?i:(chương|chapter)\s*(\d+)|第\s*(\d+)\s*章)`)

	// Heading decoration the model may emit on the first line: a # to ###### heading, or a
	// whole line wrapped in **bold**.
	headingDecorRe = regexp.MustCompile(`^\s*(?:#{1,6}\s*|\*\*)\s*|\s*\*\*\s*$`)
)

// FixChapterNumber rewrites the chapter number inside a title to the real one.
//
// Observed accident: an outline generated during planning named its chapters
// "Chương 19..23" while they were actually chapters 1..5 on disk, so a ChapterRecord held
// chapter=2 with title="Chương 20" — the record contradicted itself and the reader saw the
// table of contents skip numbers. The chapter number is a fact the engine already knows and
// must not be left to the model's memory.
//
// When the title carries no recognisable chapter number it is returned unchanged: no guessing,
// no forcing one in.
func FixChapterNumber(title string, chapter int) string {
	m := chapterNumberRe.FindStringSubmatchIndex(title)
	if m == nil || chapter <= 0 {
		return title
	}
	head := title[m[0]:m[1]]
	for _, g := range [][2]int{{m[4], m[5]}, {m[6], m[7]}} {
		if g[0] < 0 {
			continue
		}
		if n, err := strconv.Atoi(title[g[0]:g[1]]); err == nil && n != chapter {
			// Replace only the digits, keeping the original wording and case (Chương / 第…章 / Chapter).
			head = head[:g[0]-m[0]] + strconv.Itoa(chapter) + head[g[1]-m[0]:]
		}
	}
	return head + title[m[1]:]
}

// ApplyChapterHeading guarantees the first line of the prose is a canonical level-one heading.
//
// The engine already holds the correct title in ChapterFacts.Title, yet persisted the model's
// prose verbatim: of 15 chapters, 9 had no heading line at all, 1 used a level-two ## and 1
// used **bold**.
// A heading is structure, not authorship — the engine renders it uniformly, so whether the
// model writes one has no bearing on the result.
//
// The first line is replaced only when it really is a heading (equal to the title, or carrying
// a chapter-number prefix); otherwise the heading is prepended, avoiding the mistake of eating
// a prose sentence that merely begins with bold.
func ApplyChapterHeading(content, title string, chapter int) string {
	title = strings.TrimSpace(FixChapterNumber(strings.TrimSpace(title), chapter))
	if title == "" {
		return content
	}

	lines := strings.Split(content, "\n")
	first := 0
	for first < len(lines) && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	if first < len(lines) && isChapterHeadingLine(lines[first], title) {
		lines = lines[first+1:]
	} else {
		lines = lines[first:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return fmt.Sprintf("# %s\n\n%s", title, strings.Join(lines, "\n"))
}

func isChapterHeadingLine(line, title string) bool {
	bare := strings.TrimSpace(headingDecorRe.ReplaceAllString(strings.TrimSpace(line), ""))
	if bare == "" {
		return false
	}
	if strings.EqualFold(bare, title) {
		return true
	}
	// Starts with a chapter number and is short enough: a heading line, not a prose paragraph.
	return chapterNumberRe.MatchString(bare) && len([]rune(bare)) <= 80
}
