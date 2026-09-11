// Package stylestat computes book-level style statistics over written prose and produces
// pure facts.
//
// Motivation: an arc-length review window (~10 chapters) is naturally blind to book-level
// pattern ossification — sentence tics averaging dozens per chapter, identical chapter-ending
// shapes, cross-chapter repetition — because each spot looks "normal" within a single
// chapter, and only book-wide statistics can expose it. Statistics belong to code
// (deterministic, zero hallucination); judgement belongs to the LLM (the editor scores
// dimensions from the numbers, the writer uses them to self-correct).
// Compute serves one-off full calculation for offline evaluation; at runtime a Tracker
// maintains the statistics incrementally per chapter.
package stylestat

import (
	"regexp"
	"sort"
	"strings"
)

// minChapters is the floor below which no statistics are produced — the sample is too small
// for frequencies to mean anything.
const minChapters = 5

// phraseWindow limits dynamic phrase mining to the last N chapters: what the writer needs to
// avoid is "the current verbal tic".
const phraseWindow = 20

// Input is the statistics input. Chapters are in ascending chapter order; Stopwords holds
// proper nouns such as character names, skipped during dynamic phrase mining (a character's
// name is naturally frequent and is not a style problem).
type Input struct {
	Chapters  []string
	Titles    []string
	Stopwords []string
}

// Stats is the book-level style result. Every field is a factual count, with no verdict or
// instruction attached.
type Stats struct {
	Chapters          int            `json:"chapters"`
	Patterns          []PatternStat  `json:"patterns,omitempty"`
	TopPhrases        []PhraseStat   `json:"top_phrases,omitempty"`
	RepeatedSentences []SentenceStat `json:"repeated_sentences,omitempty"`
	Ending            EndingStat     `json:"ending"`
	OpeningTimeRate   float64        `json:"opening_time_rate"`
	TitleFormats      *TitleStat     `json:"title_formats,omitempty"`
}

// PatternStat counts a fixed sentence pattern book-wide (a general AI-style tic).
type PatternStat struct {
	Name       string  `json:"name"`
	Total      int     `json:"total"`
	PerChapter float64 `json:"per_chapter"`
}

// PhraseStat is a high-frequency phrase mined from the last phraseWindow chapters.
type PhraseStat struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

// SentenceStat is a long sentence repeated verbatim across chapters (direct evidence of
// replayed exposition).
type SentenceStat struct {
	Text     string `json:"text"`
	Chapters int    `json:"chapters"`
	Count    int    `json:"count"`
}

// EndingStat is the distribution of chapter-ending shapes. A short ending is legitimate in
// itself; book-wide sameness is the problem.
type EndingStat struct {
	ShortRatio  float64 `json:"short_ratio"`
	MedianRunes int     `json:"median_runes"`
}

// TitleStat counts mixed use of the "第N章" chapter-title prefix (mixing = a mechanical trace
// exposed in the output).
type TitleStat struct {
	WithPrefix    int `json:"with_prefix"`
	WithoutPrefix int `json:"without_prefix"`
}

// patternDefs holds general AI-style sentence patterns. The counts are approximate (the
// regex does no parsing); their purpose is this book's own longitudinal baseline comparison,
// where absolute precision does not matter.
//
// The patterns themselves target Chinese prose and stay as-is: they are the instrument, not
// the labels. Only their display names are translated.
var patternDefs = []struct {
	name string
	re   *regexp.Regexp
}{
	{"Câu hiệu chỉnh «không… (mà) là…»", regexp.MustCompile(`不是[^。！？\n]{1,24}?[，、]?(?:而)?是`)},
	{"Lượng từ thời gian «X hơi/X chớp»", regexp.MustCompile(`[一两二三四五六七八九十几数半][息瞬]`)},
	{"So sánh «như một/tựa như»", regexp.MustCompile(`像一|仿佛|如同|宛如`)},
	{"Nhịp im lặng «im lặng/không nói gì/không quay đầu»", regexp.MustCompile(`沉默了|没有说话|没有回头`)},
	{"Khuôn mẫu thần thái «ánh mắt lóe/khóe miệng nhếch/cắn môi»", regexp.MustCompile(`眼[中底]闪过|目光一凝|瞳孔一缩|眼眶微红|嘴角[微轻一]?[勾扬翘]|咬了咬唇|不可置信`)},
	{"Phản ứng cơ thể «lòng thắt lại/người run lên/hít khí lạnh»", regexp.MustCompile(`心头一[紧沉颤]|身子一[颤震僵]|倒吸(?:了)?一口凉气`)},
	{"Dấu hiệu suy nghĩ «thầm nghĩ/nhận ra/cảm thấy»", regexp.MustCompile(`心想|意识到|感到|觉得`)},
	{"Câu sáo trừu tượng «một nỗi không gọi được/ý nghĩa nằm ở»", regexp.MustCompile(`一种说不出的|说不清[的道]|的意义在于|真正的[^。！？\n]{1,10}是`)},
}

var (
	sentenceSplit = regexp.MustCompile(`[。！？\n]+`)
	openingTimeRe = regexp.MustCompile(`夜|清晨|黎明|天亮|醒来|晨光|一整夜`)
	titlePrefixRe = regexp.MustCompile(`^#{0,2}\s*第[零〇一二三四五六七八九十百千万\d]+章`)
)

// shortEndingRunes: a last line no longer than this counts as a "short ending".
const shortEndingRunes = 30

// Compute calculates the book-level style statistics, returning nil when there are too few chapters.
func Compute(in Input) *Stats {
	n := len(in.Chapters)
	if n < minChapters {
		return nil
	}
	all := strings.Join(in.Chapters, "\n")

	s := &Stats{Chapters: n}
	for _, def := range patternDefs {
		total := len(def.re.FindAllStringIndex(all, -1))
		if total == 0 {
			continue
		}
		s.Patterns = append(s.Patterns, PatternStat{
			Name:       def.name,
			Total:      total,
			PerChapter: round1(float64(total) / float64(n)),
		})
	}
	s.TopPhrases = minePhrases(recentWindow(in.Chapters), in.Stopwords)
	s.RepeatedSentences = repeatedSentences(in.Chapters)
	s.Ending = endingShape(in.Chapters)
	s.OpeningTimeRate = openingTimeRate(in.Chapters)
	s.TitleFormats = titleFormats(in.Titles)
	return s
}

func recentWindow(chapters []string) []string {
	if len(chapters) <= phraseWindow {
		return chapters
	}
	return chapters[len(chapters)-phraseWindow:]
}

// minePhrases mines 3-6 character high-frequency phrases within the window.
// Filtering: phrases containing punctuation/whitespace, starting or ending with a function
// word, or hitting a proper noun are dropped. Dedupe: any phrase that is a substring of an
// already-chosen phrase is discarded.
func minePhrases(chapters []string, stopwords []string) []PhraseStat {
	text := strings.Join(chapters, "\n")
	runes := []rune(text)
	threshold := max(8, len(chapters)/2)

	counts := make(map[string]int)
	for size := 3; size <= 6; size++ {
		for i := 0; i+size <= len(runes); i++ {
			gram := runes[i : i+size]
			if !validGram(gram) {
				continue
			}
			counts[string(gram)]++
		}
	}

	stopGrams := stopwordBigrams(stopwords)
	type cand struct {
		text  string
		count int
	}
	var cands []cand
	for g, c := range counts {
		if c < threshold || hitStopword(g, stopGrams) {
			continue
		}
		cands = append(cands, cand{g, c})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		// On equal frequency take the longer one (more information), then sort stably by lexicographic order.
		if len(cands[i].text) != len(cands[j].text) {
			return len(cands[i].text) > len(cands[j].text)
		}
		return cands[i].text < cands[j].text
	})

	var out []PhraseStat
	for _, c := range cands {
		if len(out) >= 8 {
			break
		}
		dup := false
		for _, picked := range out {
			if strings.Contains(picked.Text, c.text) || strings.Contains(c.text, picked.Text) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, PhraseStat{Text: c.text, Count: c.count})
		}
	}
	return out
}

// gramEdgeStop: an n-gram starting or ending with one of these function words/pronouns is not
// a style phrase and is skipped.
const gramEdgeStop = "的了着是在和与就也都还又把被他她它我你这那"

func validGram(gram []rune) bool {
	for _, r := range gram {
		if r < 0x4E00 || r > 0x9FFF { // Chinese-character runs only
			return false
		}
	}
	if strings.ContainsRune(gramEdgeStop, gram[0]) || strings.ContainsRune(gramEdgeStop, gram[len(gram)-1]) {
		return false
	}
	return true
}

// stopwordBigrams splits a proper noun into 2-character fragments: a name often appears in
// prose only partially ("九渊负手" contains "九渊"), so matching the full name would miss it.
// Over-filtering is preferable — losing one phrase fact is harmless, whereas a character name
// mixed into the verbal-tic list is noise.
func stopwordBigrams(stopwords []string) []string {
	var grams []string
	for _, w := range stopwords {
		runes := []rune(strings.TrimSpace(w))
		if len(runes) < 2 {
			continue
		}
		for i := 0; i+2 <= len(runes); i++ {
			grams = append(grams, string(runes[i:i+2]))
		}
	}
	return grams
}

func hitStopword(gram string, stopGrams []string) bool {
	for _, g := range stopGrams {
		if strings.Contains(gram, g) {
			return true
		}
	}
	return false
}

// repeatedSentences finds sentences of >=12 characters repeated verbatim across >=3 chapters,
// taking the top 5 by count.
func repeatedSentences(chapters []string) []SentenceStat {
	type rec struct {
		count    int
		chapters map[int]struct{}
	}
	seen := make(map[string]*rec)
	for ci, text := range chapters {
		for sent, count := range chapterSentenceCounts(text) {
			r := seen[sent]
			if r == nil {
				r = &rec{chapters: make(map[int]struct{})}
				seen[sent] = r
			}
			r.count += count
			r.chapters[ci] = struct{}{}
		}
	}

	var out []SentenceStat
	for sent, r := range seen {
		if len(r.chapters) < 3 {
			continue
		}
		out = append(out, SentenceStat{Text: truncateRunes(sent, 40), Chapters: len(r.chapters), Count: r.count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Text < out[j].Text
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// trimWrappedQuotes strips wrapping quotes: the same line with and without a leading quote
// must not count as two entries.
func trimWrappedQuotes(sentence string) string {
	return strings.Trim(strings.TrimSpace(sentence), `"“”‘’「」『』`)
}

func endingShape(chapters []string) EndingStat {
	var lengths []int
	short := 0
	for _, text := range chapters {
		line := lastNonEmptyLine(text)
		if line == "" {
			continue
		}
		n := len([]rune(line))
		lengths = append(lengths, n)
		if n <= shortEndingRunes {
			short++
		}
	}
	if len(lengths) == 0 {
		return EndingStat{}
	}
	sort.Ints(lengths)
	return EndingStat{
		ShortRatio:  round2(float64(short) / float64(len(lengths))),
		MedianRunes: lengths[len(lengths)/2],
	}
}

func openingTimeRate(chapters []string) float64 {
	hit := 0
	for _, text := range chapters {
		if openingTimeRe.MatchString(firstParagraph(text)) {
			hit++
		}
	}
	return round2(float64(hit) / float64(len(chapters)))
}

func titleFormats(titles []string) *TitleStat {
	if len(titles) == 0 {
		return nil
	}
	t := &TitleStat{}
	for _, title := range titles {
		if strings.TrimSpace(title) == "" {
			continue
		}
		if titlePrefixRe.MatchString(title) {
			t.WithPrefix++
		} else {
			t.WithoutPrefix++
		}
	}
	// Only mixing is worth reporting; a consistent format is not a problem in fact terms.
	if t.WithPrefix == 0 || t.WithoutPrefix == 0 {
		return nil
	}
	return t
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// firstParagraph takes the first non-empty, non-Markdown-heading line (a chapter file often
// starts with a # heading).
func firstParagraph(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
