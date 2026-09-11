package domain

import (
	"fmt"
	"strings"
)

// BookMetadata is the work information aimed at readers and publications.
// Creative settings belong to Foundation and runtime progress to Progress; neither carries this
// data.
type BookMetadata struct {
	Title    string `json:"title"`
	Synopsis string `json:"synopsis"`
}

// Normalized returns the canonical, persistable and comparable value.
func (b BookMetadata) Normalized() BookMetadata {
	b.Title = strings.TrimSpace(b.Title)
	b.Synopsis = strings.TrimSpace(b.Synopsis)
	return b
}

// Validate checks the required fields of the work metadata.
func (b BookMetadata) Validate() error {
	b = b.Normalized()
	if b.Title == "" {
		return fmt.Errorf("book title is required")
	}
	if b.Synopsis == "" {
		return fmt.Errorf("book synopsis is required")
	}
	return nil
}

// OutlineEntry is an outline entry, corresponding to one chapter.
type OutlineEntry struct {
	Chapter   int      `json:"chapter"`
	Title     string   `json:"title"`
	CoreEvent string   `json:"core_event"`
	Hook      string   `json:"hook"`
	Scenes    []string `json:"scenes"`
}

// Character is a character profile.
type Character struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"` // Aliases/titles/nicknames (e.g. "đứa phế vật", "anh Viêm")
	Role        string   `json:"role"`
	Description string   `json:"description"`
	Arc         string   `json:"arc"`
	Traits      []string `json:"traits"`
	Tier        string   `json:"tier,omitempty"` // core / important / secondary / decorative (defaults to important)
}

// VolumeOutline is the volume-level outline (layered long-form mode).
type VolumeOutline struct {
	Index int          `json:"index"`
	Title string       `json:"title"`
	Theme string       `json:"theme"`           // This volume's core conflict/theme
	Final bool         `json:"final,omitempty"` // Finale volume: the book converges here (declared by the architect via append_volume)
	Arcs  []ArcOutline `json:"arcs"`
}

// IsExpanded reports whether the volume is expanded (has arc-level structure).
func (v *VolumeOutline) IsExpanded() bool { return len(v.Arcs) > 0 }

// FinaleVolume returns the declared finale volume index, or 0 when none is declared.
// The finale fact is "the last volume carries the Final marker": once declared, the book
// enters a converging state (planning the closing threads; finishing the final volume's
// structure completes the book). If an unmarked new volume is appended afterwards, that volume
// becomes the last one and the converging state lifts naturally — so no revoke tool is needed
// and the state is always derivable from the outline data.
func FinaleVolume(volumes []VolumeOutline) int {
	if n := len(volumes); n > 0 && volumes[n-1].Final {
		return volumes[n-1].Index
	}
	return 0
}

// StoryCompass is the ending-direction compass, replacing a fixed skeleton volume list.
// The Architect may update it at each volume boundary, letting the story direction evolve as
// writing proceeds.
type StoryCompass struct {
	EndingDirection string   `json:"ending_direction"`          // Ending direction (thematic description)
	OpenThreads     []string `json:"open_threads,omitempty"`    // Open long threads (must converge before the ending)
	EstimatedScale  string   `json:"estimated_scale,omitempty"` // Rough scale (e.g. "khoảng 4-6 tập")
	LastUpdated     int      `json:"last_updated,omitempty"`    // Completed chapter count when last updated
}

// ArcOutline is the arc-level outline.
type ArcOutline struct {
	Index             int            `json:"index"` // Arc index within the volume
	Title             string         `json:"title"`
	Goal              string         `json:"goal"`                         // Arc goal (setup/development/turn/resolution)
	EstimatedChapters int            `json:"estimated_chapters,omitempty"` // Estimated chapters for a skeleton arc (cleared once expanded)
	Chapters          []OutlineEntry `json:"chapters"`
}

// IsExpanded reports whether the arc is expanded (has detailed chapters).
func (a *ArcOutline) IsExpanded() bool { return len(a.Chapters) > 0 }

// ArcExpansion is the complete plan the Architect produces for an unwritten arc at a
// structural boundary.
// Title/Goal are not a mechanical copy of the skeleton: the model may revise the plan for
// what has not happened yet according to the prose already written.
type ArcExpansion struct {
	Title    string         `json:"title"`
	Goal     string         `json:"goal"`
	Chapters []OutlineEntry `json:"chapters"`
}

// EstimatedChapterCapacity computes the internal capacity estimate of a layered outline:
// expanded arcs by their real chapter count, skeleton arcs by EstimatedChapters. It serves
// context strategy only and is not a total chapter count for the book; the chapters that are
// genuinely detailed and writable always come from FlattenOutline, and this value must never be
// exposed to the user or the model.
func EstimatedChapterCapacity(volumes []VolumeOutline) int {
	n := 0
	for _, v := range volumes {
		for _, a := range v.Arcs {
			if a.IsExpanded() {
				n += len(a.Chapters)
			} else {
				n += a.EstimatedChapters
			}
		}
	}
	return n
}

// FlattenOutline expands a layered outline into a flat chapter list, keeping global chapter numbers contiguous.
func FlattenOutline(volumes []VolumeOutline) []OutlineEntry {
	var result []OutlineEntry
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for _, e := range a.Chapters {
				e.Chapter = ch
				result = append(result, e)
				ch++
			}
		}
	}
	return result
}

// WorldRule is a worldbuilding rule entry.
type WorldRule struct {
	Category string `json:"category"` // magic / technology / geography / society / other
	Rule     string `json:"rule"`     // Rule description
	Boundary string `json:"boundary"` // Boundary that must not be violated
}

// RenumberVolumes renumbers volumes and arcs by position, starting from 1.
//
// Planning models are unreliable at writing index: starting from 0 is common, and some even
// write 0 for every arc within a volume. ExpandArc / ArcScope look up by index value, so
// duplicates or zeros leave an arc permanently unaddressable — and the symptom only surfaces a
// few steps later when expand_arc reports "invalid arguments", pointing in entirely the wrong
// direction.
//
// Array order is the fact and index is merely its name, so everything is rewritten by position
// before it is persisted.
func RenumberVolumes(volumes []VolumeOutline) {
	for vi := range volumes {
		volumes[vi].Index = vi + 1
		for ai := range volumes[vi].Arcs {
			volumes[vi].Arcs[ai].Index = ai + 1
		}
	}
}

// Repeating the same hook/core event this many times counts as an outline spinning in place.
// Twice can be a deliberate two-part structure; from three times on there is no legitimate way
// to write it — the reader is hung on the same suspense for three chapters with nobody paying
// it off.
const (
	maxHookRepeat      = 3
	maxCoreEventRepeat = 2
)

// StalledOutline detects an outline "spinning in place": the chapter titles differ, but the
// hooks or core events are the same sentence copied several times. The Writer then follows it
// faithfully — restating the previous chapter and adding a little each time, which reads as a
// rewrite rather than a continuation.
//
// Neither the Writer nor the Editor can spot this defect: both look at one chapter at a time,
// and each chapter is self-consistent on its own. It can only be caught by comparing the whole
// book where the outline is persisted.
//
// An empty string means it passed; otherwise the return value is a diagnostic the planner can
// read directly.
func StalledOutline(entries []OutlineEntry) string {
	hooks := map[string]int{}
	events := map[string]int{}
	for _, e := range entries {
		if h := strings.TrimSpace(e.Hook); h != "" {
			hooks[h]++
		}
		if c := strings.TrimSpace(e.CoreEvent); c != "" {
			events[c]++
		}
	}
	if worst, n := mostRepeated(hooks); n >= maxHookRepeat {
		return fmt.Sprintf("Đại cương chạy không tải: cùng một hook lặp ở %d/%d chương — %q. "+
			"Mỗi hook phải là hệ quả mới sinh ra ở chính chương đó và được chương sau trả; hãy viết lại từng chương", n, len(entries), truncateRunes(worst, 40))
	}
	if worst, n := mostRepeated(events); n >= maxCoreEventRepeat {
		return fmt.Sprintf("Đại cương chạy không tải: cùng một core_event lặp ở %d chương — %q. "+
			"Mỗi chương phải xảy ra việc khác nhau và làm đổi cục diện; hãy viết lại từng chương", n, truncateRunes(worst, 40))
	}
	return ""
}

func mostRepeated(m map[string]int) (string, int) {
	var key string
	best := 0
	for k, n := range m {
		if n > best {
			key, best = k, n
		}
	}
	return key, best
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// SkeletonArcs counts skeleton arcs not yet expanded, for the pre-completion check.
//
// The pre-completion check only compares against the flat outline, and the flat outline is
// derived by FlattenOutline from the expanded arcs — so skeleton arcs contribute 0 chapters and
// are entirely invisible to it. Observed accident: two skeleton arcs in volume 1 totalling 38
// chapters were never expanded, the architect jumped straight to volume 2, finished 15 chapters
// and declared the book complete, and the check waved it through because next(16) >
// len(flat)(15).
//
// Converging early still has a legitimate exit: append_volume with "final": true declares a
// finale volume.
func SkeletonArcs(volumes []VolumeOutline) []string {
	var out []string
	for vi := range volumes {
		for ai := range volumes[vi].Arcs {
			if a := &volumes[vi].Arcs[ai]; !a.IsExpanded() {
				out = append(out, fmt.Sprintf("tập %d cung %d «%s»", volumes[vi].Index, a.Index, a.Title))
			}
		}
	}
	return out
}

// maxArcChapters is the upper bound on detailed chapters in a single arc.
//
// The constraint comes from end-of-arc review, not narrative taste: at an arc boundary the
// Editor must read the whole arc before it can give a review opinion.
// Measured: a 20-chapter arc is 113,792 characters ≈ 37k tokens of prose, and once the
// outline, snapshots and prompt are added no available model can swallow it — a local 32k
// window will not fit it, a free cloud tier dropped the stream 14 times at 8 tok/s, and the
// whole pipeline eventually wedged at the arc boundary. Eight chapters ≈ 45k characters ≈ 15k
// tokens leaves headroom on both sides.
//
// It is also structurally healthier: cramming 20 chapters into one arc is itself evidence that
// the arc goal never converged.
const maxArcChapters = 8

// OversizedArc checks whether an arc is over the size limit and, if so, returns a diagnostic
// the planner can read directly. An empty string means it passed.
//
// chapters takes the larger of "detailed chapter count" and "skeleton estimated chapters": an
// arc written with estimated=20 during the skeleton stage will inevitably hit the same wall at
// expand_arc time, and that is twenty chapters too late — a structural problem must be reported
// when the structure is persisted.
func OversizedArc(label string, chapters int) string {
	if chapters <= maxArcChapters {
		return ""
	}
	return fmt.Sprintf("%s có %d chương chi tiết, vượt giới hạn %d chương mỗi cung. "+
		"Thẩm duyệt cuối cung cần đọc trọn cung một lần, cung quá dài thì không model nào duyệt nổi (thực đo 20 chương là tắc nghẽn pipeline). "+
		"Hãy tách nó thành nhiều cung có mục tiêu riêng: lần gọi này chỉ mở rộng cung đầu trong %d chương trước, "+
		"phần còn lại để dạng cung khung xương, viết tới ranh giới rồi mở tiếp",
		label, chapters, maxArcChapters, maxArcChapters)
}
