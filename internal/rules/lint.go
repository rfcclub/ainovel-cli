package rules

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Lint is the built-in product floor: it scans prose for mechanical residue, independent
// of user rules, and always runs at commit time.
// It shares Check's contract — facts only (iron rule one), never blocking a flow; the
// review pass or the user decides.
//
// Three categories today (all empirical defects from real long runs):
//   - markdown_residue: prose retaining ** bold or # heading lines beyond the first
//     (exported txt would expose the symbols).
//   - non_cjk_fragments / cjk_leak: mixed-script fragments, with the direction decided
//     automatically from the prose's dominant script (a Chinese text reports Latin
//     fragments; a Latin-script text such as Vietnamese reports Han fragments).
func Lint(text string) []Violation {
	var vs []Violation
	vs = appendMarkdownResidue(vs, text)
	vs = appendScriptMixing(vs, text)
	vs = appendBrokenWords(vs, text)
	vs = appendSelfDuplication(vs, text)
	vs = appendEnglishResidue(vs, text)
	return vs
}

func appendMarkdownResidue(vs []Violation, text string) []Violation {
	if n := strings.Count(text, "**"); n > 0 {
		vs = append(vs, Violation{
			Rule:     "markdown_residue",
			Target:   "**",
			Actual:   n,
			Severity: SeverityWarning,
		})
	}
	headings := 0
	seenContent := false
	for line := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		// A # heading on the first non-empty line is the legal chapter-file format (not
		// hard-coded to a line number, so leading blank lines are tolerated).
		first := !seenContent
		seenContent = true
		if !first && strings.HasPrefix(t, "#") {
			headings++
		}
	}
	if headings > 0 {
		vs = append(vs, Violation{
			Rule:     "markdown_residue",
			Target:   "#",
			Actual:   headings,
			Severity: SeverityWarning,
		})
	}
	return vs
}

var (
	latinFragmentRe = regexp.MustCompile(`[A-Za-z]{2,}`)
	// CJK punctuation included: what leaks into Vietnamese prose is not only Han characters
	// but full-width punctuation such as 。，、？！. An isolated 。 was observed in the prose
	// of chapter 4 — a pure Han-character rule cannot see it at all.
	cjkFragmentRe = regexp.MustCompile(`[\p{Han}\x{3000}-\x{303f}\x{ff01}-\x{ff5e}]+`)
)

// appendScriptMixing reports fragments of "the other script" mixed into the prose.
//
// Which script counts as mixed in is decided by the prose itself, never by configuration:
// a bare "pattern" inside Chinese prose is a defect, whereas every Vietnamese word is Latin
// letters, so the same rule would fire thousands of times per chapter and drown the review
// context — one chapter was measured at 1021 hits with not a single Han character in it.
// Conversely, a Han phrase leaking into Vietnamese prose is a genuine defect that the old
// rule could not see at all.
//
// So the dominant script is determined first from character share, and only the minority is
// reported. Both genres hold on their own, and a mis-written config cannot disable it.
// Legitimate loanwords (brand names, abbreviations) still fire — a warning-level fact for
// the review pass to decide on.
func appendScriptMixing(vs []Violation, text string) []Violation {
	latin := latinFragmentRe.FindAllString(text, -1)
	han := cjkFragmentRe.FindAllString(text, -1)

	// What is compared is the "Han character count" against the "Latin word count", not the
	// character counts of both sides: one Han character is roughly one word, while a Latin
	// word has several letters. Comparing characters would let a few "pattern"/"DNA" tokens
	// in Chinese prose push the Latin character count past the Han count and misjudge the
	// prose language.
	rule, matches := "non_cjk_fragments", latin
	if runeCount(han) <= len(latin) {
		// The prose is Latin script (Vietnamese etc.): Han is what mixed in.
		rule, matches = "cjk_leak", han
	}
	if len(matches) == 0 {
		return vs
	}

	seen := make(map[string]struct{})
	var examples []string
	for _, m := range matches {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		if len(examples) < 3 {
			examples = append(examples, m)
		}
	}
	return append(vs, Violation{
		Rule:     rule,
		Target:   strings.Join(examples, "、"),
		Actual:   len(matches),
		Severity: SeverityWarning,
	})
}

func runeCount(ss []string) int {
	n := 0
	for _, s := range ss {
		n += utf8.RuneCountInString(s)
	}
	return n
}

// brokenWordRe matches a word cut in half by a paragraph break: a line ending in a letter,
// then a blank line, then a lowercase letter. Observed in Vietnamese prose: "n Tông, Ng"
// + blank line + "ọc Lâm dừng bước" — the name Ngọc split in two.
// In a Latin script a word never spans paragraphs, so this shape has no legitimate use and
// can be judged a defect outright.
var brokenWordRe = regexp.MustCompile(`(?m)[\p{L}]\n\s*\n[[:space:]]*[\p{Ll}]`)

func appendBrokenWords(vs []Violation, text string) []Violation {
	matches := brokenWordRe.FindAllString(text, -1)
	if len(matches) == 0 {
		return vs
	}
	return append(vs, Violation{
		Rule:     "broken_word",
		Target:   strings.Join(strings.Fields(matches[0]), "⏎"),
		Actual:   len(matches),
		Severity: SeverityWarning,
	})
}

// paraMinRunes is the paragraph length floor for duplicate comparison. Short paragraphs
// repeat naturally ("Hắn gật đầu.", a lone "Bắt đầu!"); only full passages recurring
// verbatim indicate a generation accident.
const paraMinRunes = 20

// paragraphs cuts out the prose paragraphs long enough to be worth comparing, skipping heading lines.
func paragraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(text, "\n\n") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "#") {
			continue
		}
		if utf8.RuneCountInString(p) >= paraMinRunes {
			out = append(out, p)
		}
	}
	return out
}

// selfDupThreshold / crossDupThreshold cap the share of repeated characters in a chapter.
//
// The thresholds come from measurement: in a 22-chapter book, 19 chapters scored 0.0% on
// both, while the three problem cases were 43.5% self-duplication (chapter 21, the same
// paragraph five times), 32.2% cross-chapter (chapter 14 copying chapter 13) and 12.9%
// (chapter 22 copying chapter 21). The signal is binary with no grey zone in between, so
// 10% is chosen to leave ample room for deliberate refrains or incantations.
const (
	selfDupThreshold  = 0.10
	crossDupThreshold = 0.10
)

func appendSelfDuplication(vs []Violation, text string) []Violation {
	paras := paragraphs(text)
	if len(paras) == 0 {
		return vs
	}
	count := make(map[string]int, len(paras))
	total := 0
	for _, p := range paras {
		count[p]++
		total += utf8.RuneCountInString(p)
	}
	dup, worst, worstN := 0, "", 0
	for p, n := range count {
		if n > 1 {
			dup += utf8.RuneCountInString(p) * (n - 1)
			if n > worstN {
				worst, worstN = p, n
			}
		}
	}
	if total == 0 || float64(dup)/float64(total) < selfDupThreshold {
		return vs
	}
	return append(vs, Violation{
		Rule:     "self_duplication",
		Target:   fmt.Sprintf("×%d %s", worstN, truncateRunes(worst, 50)),
		Limit:    fmt.Sprintf("<%.0f%%", selfDupThreshold*100),
		Actual:   fmt.Sprintf("%.0f%%", 100*float64(dup)/float64(total)),
		Severity: SeverityError,
	})
}

// CheckAgainstPrevious detects "the new chapter is really a copy of the previous one".
//
// The Writer has read_chapter, so it first looks at what the previous chapter contains and
// then copies it as the continuation — measured at 32.2% of chapter 14's characters coming
// verbatim from chapter 13, and 12.9% of chapter 22 from chapter 21.
//
// self_duplication cannot see this: it only measures within a single chapter, and the
// copied chapter is internally spotless. So the comparison must be made separately against
// earlier text. It is skipped when previous is empty.
func CheckAgainstPrevious(text string, previous []string) []Violation {
	cur := paragraphs(text)
	if len(cur) == 0 || len(previous) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	for _, prev := range previous {
		for _, p := range paragraphs(prev) {
			seen[p] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}

	dup, total, sample := 0, 0, ""
	for _, p := range unique(cur) {
		n := utf8.RuneCountInString(p)
		total += n
		if _, ok := seen[p]; ok {
			dup += n
			if sample == "" {
				sample = p
			}
		}
	}
	if total == 0 || float64(dup)/float64(total) < crossDupThreshold {
		return nil
	}
	return []Violation{{
		Rule:     "copied_previous_chapter",
		Target:   truncateRunes(sample, 60),
		Limit:    fmt.Sprintf("<%.0f%%", crossDupThreshold*100),
		Actual:   fmt.Sprintf("%.0f%%", 100*float64(dup)/float64(total)),
		Severity: SeverityError,
	}}
}

func unique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0:0]
	for _, x := range in {
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// englishFunctionWordRe only collects English function words — these never enter Vietnamese
// prose as loanwords. Content words (flow / lesson / footsteps and the like) are deliberately
// excluded: they can be legitimate borrowings in a contemporary setting, and phrases such as
// "the flow" are already caught by the "the" in them, so there is no need to risk false
// positives.
var englishFunctionWordRe = regexp.MustCompile(
	`(?i)\b(not|the|and|but|for|from|with|they|this|that|just|one|was|were|have|when|which|while|into|over)\b`)

// vietnameseMarkRe matches letters unique to Vietnamese: the seven base letters (ăâđêôơư,
// upper case included) plus the Latin Extended Additional block U+1EA0-U+1EF9, which is
// almost exclusively reserved for Vietnamese tone letters.
// Listing just a few precomposed characters is not enough: most tone letters in an ordinary
// Vietnamese sentence fall inside that block.
var vietnameseMarkRe = regexp.MustCompile(`[ăâđêôơưĂÂĐÊÔƠƯ\x{1ea0}-\x{1ef9}]`)

// vietnameseMarkFloor is the number of unique letters required to judge "the prose is
// Vietnamese". A real chapter has hundreds or thousands; the floor exists only to keep this
// rule completely silent on English or Chinese works.
const vietnameseMarkFloor = 12

// appendEnglishResidue reports English function words carried inside Vietnamese prose.
//
// cjk_leak is powerless here: Vietnamese and English share the Latin alphabet, so a stray
// "not"/"the" cannot be distinguished from the prose at the character level. Nor is it
// trivial — chapter 18 was measured with 27 occurrences of "not", 9 of "the" and 3 of
// "from", for instance "Lá cây bắt đầu chuyển động—not nhanh chóng mà nhẹ nhàng".
func appendEnglishResidue(vs []Violation, text string) []Violation {
	if len(vietnameseMarkRe.FindAllString(text, vietnameseMarkFloor)) < vietnameseMarkFloor {
		return vs
	}
	matches := englishFunctionWordRe.FindAllString(text, -1)
	if len(matches) == 0 {
		return vs
	}
	seen := make(map[string]struct{})
	var examples []string
	for _, m := range matches {
		low := strings.ToLower(m)
		if _, ok := seen[low]; ok {
			continue
		}
		seen[low] = struct{}{}
		if len(examples) < 4 {
			examples = append(examples, low)
		}
	}
	return append(vs, Violation{
		Rule:     "english_residue",
		Target:   strings.Join(examples, ", "),
		Actual:   len(matches),
		Severity: SeverityError,
	})
}
