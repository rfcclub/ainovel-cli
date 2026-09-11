// Package userrules is the service layer for normalising user rules: natural-language rules from
// each source go through an LLM structured call into candidate structured fields, which
// rules.BuildSnapshot then merges deterministically into this book's snapshot.
//
// Layered responsibilities:
//   - the rules package: pure data plus deterministic merging
//     (Snapshot / Candidate / BuildSnapshot / SystemDefaults).
//   - this package: LLM normalisation plus orchestration plus persistence (depends on agentcore,
//     store and rules).
//
// Normalisation is an enhancement path, not a precondition for writing: any source failing
// degrades to raw preferences and the main writing flow must continue.
package userrules

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/rules"
)

// normalizeMaxTokens is the output cap for one normalisation (thinking tokens and the JSON output
// share this budget).
// The normalisation JSON itself is tiny (usually <1k); the bulk is headroom for reasoning models
// that cannot turn thinking off — too narrow a cap lets thinking eat into the JSON, truncating it
// and failing the parse. max_tokens is a cap, not a billed amount, so raising it costs nothing.
const normalizeMaxTokens = 8192

// normalizeContract sits next to its boundary DTO: every field required, and fatigue_words
// expressed as an array of objects (strict mode forbids maps with dynamic keys). Both modes share
// the same DTO convention.
var normalizeContract = llmcontract.Contract{
	Name:        "userrules_normalize",
	Description: "Chuẩn hóa quy tắc viết bằng ngôn ngữ tự nhiên của người dùng thành các trường có cấu trúc",
	Schema: schema.Object(
		schema.Property("structured", schema.Object(
			schema.Property("genre", schema.String("Thể loại; không có thì để chuỗi rỗng")).Required(),
			schema.Property("forbidden_chars", schema.Array("Ký tự bị cấm xuất hiện", schema.String("Ký tự"))).Required(),
			schema.Property("forbidden_phrases", schema.Array("Cụm từ bị cấm xuất hiện (khớp chính xác từng chữ)", schema.String("Cụm từ"))).Required(),
			schema.Property("fatigue_words", schema.Array("Từ nhàm và số lần xuất hiện tối đa mỗi chương", schema.Object(
				schema.Property("word", schema.String("Từ nhàm")).Required(),
				schema.Property("max_per_chapter", schema.Int("Số lần xuất hiện tối đa mỗi chương (số nguyên dương)")).Required(),
			))).Required(),
		)).Required(),
		schema.Property("preferences", schema.String("Sở thích về văn phong/nhân vật/thẩm mỹ bằng ngôn ngữ tự nhiên; không có thì để chuỗi rỗng")).Required(),
		schema.Property("uncertain", schema.Array("Các mục cố ý không nâng vào structured kèm lý do", schema.String("Mục"))).Required(),
	),
}

// Normalizer turns one source's natural-language rules into a rules.Candidate.
type Normalizer struct {
	model agentcore.ChatModel
}

// NewNormalizer builds a normaliser around one ChatModel. Normalisation is a one-off startup
// tool, so pass a capable model (such as the ModelSet default) rather than following the weaker
// writing model.
//
// Normalisation does not override thinking: an explicit off is itself a reasoning parameter only
// some models support, and an ordinary chat model rejects it. Keep the provider/model default and
// let normalizeMaxTokens reserve output budget for models whose thinking cannot be disabled.
func NewNormalizer(model agentcore.ChatModel) *Normalizer {
	return &Normalizer{model: model}
}

// Normalize normalises one source. On failure it returns an error (with the real cause) and the
// caller decides whether to degrade (Service.normalizeOrDegrade records a degraded candidate) —
// a technical error no longer masquerades as a normal result, and terminal errors (auth,
// permission and so on) are not retried.
func (n *Normalizer) Normalize(ctx context.Context, source, text string) (rules.Candidate, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return rules.Candidate{Source: source}, nil
	}
	if n == nil || n.model == nil {
		return rules.Candidate{}, fmt.Errorf("chưa cấu hình model chuẩn hóa")
	}

	out, err := llmcontract.Execute(ctx, n.model, llmcontract.Request[normalizerOutput]{
		Contract:     normalizeContract,
		SystemPrompt: normalizerSystemPrompt,
		Payload:      text,
		Options:      []agentcore.CallOption{agentcore.WithMaxTokens(normalizeMaxTokens)},
		Validate: func(out *normalizerOutput) error {
			_, err := out.toCandidate(source)
			return err
		},
		Agent: "rules",
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				slog.Debug("chọn giao thức chuẩn hóa quy tắc", "module", "rules", "source", source,
					"contract", normalizeContract.Name, "structured_mode", res.Mode,
					"capability_source", res.Source, "provider", res.Provider, "model", res.Model,
					"schema_fingerprint", normalizeContract.Fingerprint())
			},
			Correction: func(ev llmcontract.Correction) {
				slog.Warn("tự sửa kết quả chuẩn hóa quy tắc", "module", "rules", "source", source,
					"attempt", ev.Attempt, "layer", ev.Layer, "structured_mode", ev.Mode, "err", ev.Err)
			},
		},
	})
	if err != nil {
		return rules.Candidate{}, fmt.Errorf("chuẩn hóa thất bại: %w", err)
	}
	return out.toCandidate(source)
}

// degraded builds a degraded candidate: when normalisation fails the raw text becomes the style
// preference and no mechanical rule is distilled. uncertain marks the source (so the UI can
// report "which sources could not be parsed") but carries no technical error detail — those go to
// the log only.
func degraded(source, text string) rules.Candidate {
	return rules.Candidate{
		Source:      source,
		Preferences: text,
		Uncertain:   []string{source + ": chuẩn hóa thất bại, đã xử lý nguyên văn như sở thích văn phong (chưa trích xuất quy tắc cơ học)"},
		Degraded:    true,
	}
}

// normalizerOutput is the boundary DTO the normaliser agrees on (shared by both modes): uncertain
// is always a string array and fatigue_words always an array of objects — the shape is pinned by
// the contract, so there is no guessing between variants.
type normalizerOutput struct {
	Structured  normalizerStructured `json:"structured"`
	Preferences string               `json:"preferences"`
	Uncertain   []string             `json:"uncertain"`
}

type normalizerStructured struct {
	Genre            string             `json:"genre"`
	ForbiddenChars   []string           `json:"forbidden_chars"`
	ForbiddenPhrases []string           `json:"forbidden_phrases"`
	FatigueWords     []fatigueWordEntry `json:"fatigue_words"`
}

type fatigueWordEntry struct {
	Word          string `json:"word"`
	MaxPerChapter int    `json:"max_per_chapter"`
}

// toCandidate validates the boundary DTO and converts it into a domain candidate: each fatigue
// entry needs a non-empty word and a positive integer cap (validation errors can be fed back to
// the model for correction), and the domain side stays a map[string]int.
func (o normalizerOutput) toCandidate(source string) (rules.Candidate, error) {
	var fatigue map[string]int
	for _, e := range o.Structured.FatigueWords {
		word := strings.TrimSpace(e.Word)
		if word == "" {
			return rules.Candidate{}, fmt.Errorf("fatigue_words chứa mục từ rỗng")
		}
		if e.MaxPerChapter < 1 {
			return rules.Candidate{}, fmt.Errorf("fatigue_words[%q].max_per_chapter phải là số nguyên dương, nhận %d", word, e.MaxPerChapter)
		}
		if fatigue == nil {
			fatigue = make(map[string]int, len(o.Structured.FatigueWords))
		}
		fatigue[word] = e.MaxPerChapter
	}
	return rules.Candidate{
		Source: source,
		Structured: rules.Structured{
			Genre:            strings.TrimSpace(o.Structured.Genre),
			ForbiddenChars:   nonEmpty(o.Structured.ForbiddenChars),
			ForbiddenPhrases: nonEmpty(o.Structured.ForbiddenPhrases),
			FatigueWords:     fatigue,
		},
		Preferences: strings.TrimSpace(o.Preferences),
		Uncertain:   nonEmpty(o.Uncertain),
	}, nil
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// normalizerSystemPrompt only describes normalisation semantics; the output structure
// is maintained at a single point by normalizeContract.
// Ten real examples (including the invented-threshold trap) confirmed the conservative
// promotion rule holds (10/10).
const normalizerSystemPrompt = `Bạn là "Bộ chuẩn hóa quy tắc" của hệ thống viết tiểu thuyết AI. Bạn đọc quy tắc viết dài hạn từ một nguồn của người dùng (ngôn ngữ tự nhiên), nâng những quy tắc rõ ràng và có thể kiểm tra cơ học vào structured, phần còn lại đưa vào preferences hoặc uncertain.

【Nâng thận trọng — quan trọng nhất】
- Chỉ ghi vào structured khi người dùng nói rõ ràng, không mơ hồ.
- forbidden_chars/forbidden_phrases là mức error: chỉ nâng khi có lệnh cấm rõ ràng kiểu "đừng xuất hiện X / cấm dùng X / chớ viết X".
- fatigue_words: chỉ nâng khi có đồng thời "từ cụ thể" và "ngưỡng số lần cụ thể"; "bớt dùng X / đừng lúc nào cũng dùng X" mà không có số thì đưa vào preferences, tuyệt đối không tự bịa ngưỡng.
- Mọi mong muốn về số chữ/độ dài ("mỗi chương 3000 chữ", "ngắn một chút") đều đưa vào preferences: độ dài chương là vấn đề nhịp điệu tự sự, do lúc sáng tác tự nắm bắt, không kiểm tra cơ học.
- Những gì không kiểm tra được bằng máy, không có ngưỡng rõ ràng, phụ thuộc ngữ cảnh thì đưa hết vào preferences.
- Nguyên tắc: thà bỏ sót khỏi structured còn hơn nâng sai (nâng sai sẽ báo lỗi giả ở mọi chương).

preferences giữ sở thích về văn phong, nhân vật và thẩm mỹ bằng một đoạn ngôn ngữ tự nhiên dễ đọc.
uncertain nêu rõ những mục bạn cố ý không nâng vào structured kèm lý do.`
