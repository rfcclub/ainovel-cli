package exp

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// chapterTitleIndex looks up a title by chapter number, returning an empty string when absent.
type chapterTitleIndex map[int]string

func buildTitleIndex(outline []domain.OutlineEntry) chapterTitleIndex {
	idx := make(chapterTitleIndex, len(outline))
	for _, e := range outline {
		if e.Title != "" {
			idx[e.Chapter] = e.Title
		}
	}
	return idx
}

// chapterLocation is where a chapter sits in the layered outline. Only the volume information
// the export layout needs is kept — arcs never enter the export (from a reader's view arcs are
// an over-fine internal structure).
type chapterLocation struct {
	VolumeIdx       int
	VolumeTitle     string
	IsFirstOfVolume bool
}

// buildLocations builds {chapter -> location} in the layered outline's global chapter order.
// Chapter numbers are rebuilt by the same rule as FlattenOutline (accumulated in volume then arc
// order) to stay consistent with Progress.CompletedChapters. The arc level is still walked (it
// is unavoidable for computing global chapter numbers) but never lands in a location — the
// export only inserts a divider at the start of a volume.
func buildLocations(volumes []domain.VolumeOutline) map[int]chapterLocation {
	if len(volumes) == 0 {
		return nil
	}
	locs := make(map[int]chapterLocation)
	ch := 0
	for _, v := range volumes {
		firstOfVol := true
		for _, a := range v.Arcs {
			for range a.Chapters {
				ch++
				locs[ch] = chapterLocation{
					VolumeIdx:       v.Index,
					VolumeTitle:     v.Title,
					IsFirstOfVolume: firstOfVol,
				}
				firstOfVol = false
			}
		}
	}
	return locs
}

// chapterHeaderRe matches a first line that is a Markdown heading carrying a chapter number
// (# 第N章 / ## 第 12 章 ...).
var chapterHeaderRe = regexp.MustCompile(`^#+\s+第.+?章`)

// atxTitleRe extracts the text part of an ATX heading (# Title).
var atxTitleRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)

// stripChapterTitleHeader strips the first line when it is a chapter heading that would
// duplicate the exporter's uniform title.
// Two cases: (1) "# 第N章 …" (carrying a chapter number); (2) a Markdown heading whose text is
// exactly this chapter's title (the writer often puts a bare chapter name on the first line of
// the prose, e.g. "# 边村浮生", duplicating the exporter's "Chương N 边村浮生"). Any other h1
// (such as "# 序章") counts as part of the prose and is kept.
// The caller trims first, so leading blank lines are out of scope.
func stripChapterTitleHeader(content, title string) string {
	first, rest, hasNewline := strings.Cut(content, "\n")
	if !isChapterTitleLine(first, title) {
		return content
	}
	if !hasNewline {
		return ""
	}
	return strings.TrimLeft(rest, "\n")
}

func isChapterTitleLine(line, title string) bool {
	if chapterHeaderRe.MatchString(line) {
		return true
	}
	if title = strings.TrimSpace(title); title == "" {
		return false
	}
	m := atxTitleRe.FindStringSubmatch(line)
	return len(m) == 2 && strings.TrimSpace(m[1]) == title
}

// renderTXT assembles the final text.
//
// Chapter order comes from chapters (the caller has already deduplicated and sorted by chapter
// number). bodies/titleIdx/locations all degrade on absence: a missing title emits only
// "Chương N", and a missing layered location falls back to a flat outline.
func renderTXT(
	novelName string,
	chapters []int,
	titleIdx chapterTitleIndex,
	locations map[int]chapterLocation,
	bodies map[int]string,
) string {
	var b strings.Builder

	if name := strings.TrimSpace(novelName); name != "" {
		b.WriteString("《")
		b.WriteString(name)
		b.WriteString("》\n\n")
	}

	useLayered := len(locations) > 0

	for i, ch := range chapters {
		if useLayered {
			if loc, ok := locations[ch]; ok && loc.IsFirstOfVolume {
				b.WriteString("\n═══════════════════════════════════════════\n")
				fmt.Fprintf(&b, "           Tập %d  %s\n", loc.VolumeIdx, strings.TrimSpace(loc.VolumeTitle))
				b.WriteString("═══════════════════════════════════════════\n\n")
			}
		}

		title := strings.TrimSpace(titleIdx[ch])
		if title != "" {
			fmt.Fprintf(&b, "Chương %d  %s\n\n", ch, title)
		} else {
			fmt.Fprintf(&b, "Chương %d\n\n", ch)
		}

		body := stripChapterTitleHeader(strings.TrimSpace(bodies[ch]), title)
		b.WriteString(body)
		b.WriteString("\n")
		if i < len(chapters)-1 {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}
