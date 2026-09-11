package userrules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func TestExtractJSON_StripsCodeFences(t *testing.T) {
	cases := []struct{ in, wantHas string }{
		{"```json\n{\"a\":1}\n```", `"a":1`},
		{"```\n{\"a\":1}\n```", `"a":1`},
		{"前缀解释\n{\"a\":1}\n后缀", `"a":1`},
		{"{\"a\":1}", `"a":1`},
	}
	for _, c := range cases {
		got := llmcontract.ExtractJSONObject(c.in)
		if got == "" {
			t.Fatalf("extractJSON(%q) 返回空", c.in)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("extractJSON(%q)=%q 不是合法 JSON: %v", c.in, got, err)
		}
	}
	if llmcontract.ExtractJSONObject("没有任何 JSON") != "" {
		t.Fatal("无 JSON 时应返回空串")
	}
}

func TestParseNormalizerJSON_FullOutput(t *testing.T) {
	raw := "```json\n" + `{
  "structured": {
    "genre": "都市",
    "forbidden_chars": [],
    "forbidden_phrases": ["某种程度上"],
    "fatigue_words": [{"word": "竟然", "max_per_chapter": 2}]
  },
  "preferences": "主角冷静克制",
  "uncertain": ["少用比喻：无阈值"]
}` + "\n```"
	body := llmcontract.ExtractJSONObject(raw)
	if err := llmcontract.ValidateJSON(normalizeContract.Schema, []byte(body)); err != nil {
		t.Fatalf("应解析成功: %v", err)
	}
	var out normalizerOutput
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("应解码成功: %v", err)
	}
	cand, err := out.toCandidate("startup_prompt")
	if err != nil {
		t.Fatalf("toCandidate: %v", err)
	}
	if cand.Structured.Genre != "都市" {
		t.Fatalf("genre 解析错误：%+v", cand.Structured)
	}
	if len(cand.Structured.ForbiddenPhrases) != 1 || cand.Structured.ForbiddenPhrases[0] != "某种程度上" {
		t.Fatalf("forbidden_phrases 解析错误：%v", cand.Structured.ForbiddenPhrases)
	}
	if cand.Structured.FatigueWords["竟然"] != 2 {
		t.Fatalf("fatigue_words 数组应转成 map：%v", cand.Structured.FatigueWords)
	}
	if cand.Preferences != "主角冷静克制" {
		t.Fatalf("preferences 解析错误：%q", cand.Preferences)
	}
	if len(cand.Uncertain) != 1 {
		t.Fatalf("uncertain 应有 1 条，得到 %v", cand.Uncertain)
	}
}

// fatigue entry validation: an empty word and a non-positive threshold are both business errors the model can be told to fix.
func TestToCandidateRejectsInvalidFatigueEntries(t *testing.T) {
	bad := normalizerOutput{Structured: normalizerStructured{
		FatigueWords: []fatigueWordEntry{{Word: " ", MaxPerChapter: 2}},
	}}
	if _, err := bad.toCandidate("x"); err == nil {
		t.Fatal("空词条目应报错")
	}
	bad = normalizerOutput{Structured: normalizerStructured{
		FatigueWords: []fatigueWordEntry{{Word: "竟然", MaxPerChapter: 0}},
	}}
	if _, err := bad.toCandidate("x"); err == nil {
		t.Fatal("非正整数阈值应报错")
	}
}

func TestParseNormalizerJSON_GarbageFails(t *testing.T) {
	if body := llmcontract.ExtractJSONObject("模型只回了一句话，没有 JSON"); body != "" {
		t.Fatal("无 JSON 应解析失败（触发降级）")
	}
	if body := llmcontract.ExtractJSONObject("{ 不完整"); body != "" {
		t.Fatal("残缺 JSON 应解析失败")
	}
}

// Contract test (RFC §11.1): the root is an object and every property (including nested structured/fatigue_words entries) is required.
func TestNormalizeContractIsStrictReady(t *testing.T) {
	if normalizeContract.Schema["type"] != "object" {
		t.Fatal("根必须是 object")
	}
	if err := llmcontract.ValidateStrictReady(normalizeContract.Schema); err != nil {
		t.Fatal(err)
	}
}

func TestNormalize_NilModelErrors(t *testing.T) {
	// No model available: return an explicit error and let the Service layer degrade to raw preferences.
	var n *Normalizer = NewNormalizer(nil)
	if _, err := n.Normalize(t.Context(), "startup_prompt", "每章1200字，主角冷静"); err == nil {
		t.Fatal("无模型应返回错误")
	}
}

// scriptedModel is a minimal fake ChatModel: it emits preset replies in call order and records the
// messages received in the last round, so a test can assert whether a feedback retry merged the
// correction into the next round's conversation. Replies repeat the last one once exhausted.
type scriptedModel struct {
	replies  []string
	calls    int
	lastMsgs []agentcore.Message
	lastCfg  agentcore.CallConfig
	err      error // 非 nil 时 Generate 恒返回该错误
	cancel   context.CancelFunc
	cancelAt int
}

func (m *scriptedModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	var cfg agentcore.CallConfig
	for _, o := range opts {
		o(&cfg)
	}
	m.lastCfg = cfg
	m.lastMsgs = messages
	m.calls++
	if m.cancel != nil && m.cancelAt > 0 && m.calls >= m.cancelAt {
		m.cancel()
	}
	if m.err != nil {
		return nil, m.err
	}
	i := m.calls - 1
	if i >= len(m.replies) {
		i = len(m.replies) - 1
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(m.replies[i])},
	}}, nil
}

func (m *scriptedModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, nil
}

func (m *scriptedModel) SupportsTools() bool { return false }

// Feedback-style retry: the first round emits bad JSON, the second is valid. Normalize
// must succeed and the second round must carry the previous bad output plus the
// correction hint (feedback, not a blind retry of the same request).
func TestNormalize_FeedbackRetryRecovers(t *testing.T) {
	model := &scriptedModel{replies: []string{
		"这不是 JSON",
		`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":["某种程度上"],"fatigue_words":[]},"preferences":"","uncertain":[]}`,
	}}
	n := NewNormalizer(model)

	cand, err := n.Normalize(t.Context(), "startup_prompt", "不要出现某种程度上")
	if err != nil {
		t.Fatalf("次轮已返回合法 JSON，不应失败: %v", err)
	}
	if len(cand.Structured.ForbiddenPhrases) != 1 {
		t.Fatalf("应解析出 forbidden_phrases，got %+v", cand.Structured)
	}
	if model.calls != 2 {
		t.Fatalf("应在第 2 次成功，实际调用 %d 次", model.calls)
	}

	var sawBad, sawHint bool
	for _, msg := range model.lastMsgs {
		text := msg.TextContent()
		if text == "这不是 JSON" {
			sawBad = true
		}
		if strings.Contains(text, "JSON Schema") && strings.Contains(text, "Lỗi:") {
			sawHint = true
		}
	}
	if !sawBad || !sawHint {
		t.Errorf("次轮应并入上一轮坏输出与纠正提示，sawBad=%v sawHint=%v", sawBad, sawHint)
	}
	system := model.lastMsgs[0].TextContent()
	if !strings.Contains(system, "<output-json-schema>") || !strings.Contains(system, `"fatigue_words"`) {
		t.Fatalf("prompt contract 应从 Contract 自动附加 schema:\n%s", system)
	}
}

// Normalisation does not override the model's thinking default; an ordinary chat model rejects an explicit off.
func TestNormalize_LeavesThinkingUnspecifiedAndReservesTokens(t *testing.T) {
	model := &scriptedModel{replies: []string{`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`}}
	n := NewNormalizer(model)

	if _, err := n.Normalize(t.Context(), "startup_prompt", "随便一条规则"); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if model.lastCfg.ThinkingLevel != agentcore.ThinkingAuto {
		t.Errorf("不应发送 thinking 参数，got %q", model.lastCfg.ThinkingLevel)
	}
	if model.lastCfg.MaxTokens != normalizeMaxTokens {
		t.Errorf("max_tokens 应为 %d，got %d", normalizeMaxTokens, model.lastCfg.MaxTokens)
	}
}

// Bad JSON throughout: the feedback loop must stop after a bounded number of rounds. Making
// context cancellation the only exit turned a provider that never returns valid JSON into an
// unkillable hang — each round is a full model call, so normalize blocked the whole boundary.
func TestNormalize_FeedbackRetryStopsAfterBoundedRounds(t *testing.T) {
	model := &scriptedModel{replies: []string{"hỏng"}}
	n := NewNormalizer(model)

	_, err := n.Normalize(context.Background(), "startup_prompt", "mỗi chương 1200 chữ")
	if err == nil {
		t.Fatal("đầu ra không bao giờ hợp lệ phải thất bại, không được hỏi mãi")
	}
	if model.calls == 0 || model.calls > 10 {
		t.Fatalf("phải dừng sau số lần hữu hạn, actual %d", model.calls)
	}
}

type terminalTestError struct{}

func (terminalTestError) Error() string   { return "401 authentication failed" }
func (terminalTestError) Retryable() bool { return false }

type retryableTestError struct{}

func (retryableTestError) Error() string             { return "provider unavailable" }
func (retryableTestError) Retryable() bool           { return true }
func (retryableTestError) RetryAfter() time.Duration { return time.Millisecond }

// A terminal error (401 and the like) must not be retried blindly: exactly one call then an error.
func TestNormalize_TerminalErrorStopsImmediately(t *testing.T) {
	model := &scriptedModel{err: terminalTestError{}}
	n := NewNormalizer(model)

	_, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err == nil || !errors.As(err, &terminalTestError{}) {
		t.Fatalf("应透出终止错误: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("终止错误不应重试，实际调用 %d 次", model.calls)
	}
}

// A retryable request error is retried with backoff by llmretry.
type flakyModel struct {
	scriptedModel
	failures int
}

func (m *flakyModel) Generate(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if m.scriptedModel.calls < m.failures {
		m.scriptedModel.calls++
		return nil, retryableTestError{}
	}
	return m.scriptedModel.Generate(ctx, msgs, tools, opts...)
}

func TestNormalize_RetryableErrorRecovers(t *testing.T) {
	model := &flakyModel{
		scriptedModel: scriptedModel{replies: []string{`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`}},
		failures:      2,
	}
	n := NewNormalizer(model)
	cand, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err != nil || cand.Preferences != "x" {
		t.Fatalf("退避后应成功: %+v %v", cand, err)
	}
}

// nativeRulesModel is declared to support native JSON Schema.
type nativeRulesModel struct {
	*scriptedModel
}

func (m *nativeRulesModel) Capabilities() llm.Capabilities {
	return llm.Capabilities{
		Provider:   "openai",
		Model:      "gpt-test",
		Structured: llm.StructuredCapabilities{JSONSchema: llm.SupportYes, Strict: llm.SupportYes},
	}
}

func TestNormalize_NativeSendsSchemaAndRejectsFences(t *testing.T) {
	// Native mode: the schema goes in the request and bare JSON succeeds.
	model := &nativeRulesModel{&scriptedModel{replies: []string{
		`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`,
	}}}
	n := NewNormalizer(model)
	cand, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err != nil || cand.Preferences != "x" {
		t.Fatalf("native 归一化失败: %+v %v", cand, err)
	}
	rf := model.lastCfg.ResponseFormat
	if rf == nil || rf.JSONSchema == nil || rf.JSONSchema.Name != "userrules_normalize" {
		t.Fatalf("native 模式应发送 schema: %+v", rf)
	}
	if got := model.lastMsgs[0].TextContent(); got != normalizerSystemPrompt {
		t.Fatalf("native 模式不应向提示词重复注入 schema:\n%s", got)
	}

	// Fenced output = contract breach: error at once, with no extractJSON fallback and no re-ask.
	fenced := &nativeRulesModel{&scriptedModel{replies: []string{
		"```json\n{\"structured\":{},\"preferences\":\"x\",\"uncertain\":[]}\n```",
	}}}
	n = NewNormalizer(fenced)
	_, err = n.Normalize(t.Context(), "startup_prompt", "规则")
	if err == nil || !strings.Contains(err.Error(), "vi phạm contract") {
		t.Fatalf("期望契约违约错误, got %v", err)
	}
	if fenced.calls != 1 {
		t.Fatalf("契约违约不应重问，实际 %d 次", fenced.calls)
	}
}
