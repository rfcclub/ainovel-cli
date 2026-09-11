package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/agents/guard"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// agentToRole normalises a subagent name into the role name ModelSet recognises.
// architect_short / architect_long both share the single architect role configuration.
// It is synonymous with host.agentRoleName; build and host do not depend on each other, so
// each keeps its own copy.
func agentToRole(name string) string {
	if strings.HasPrefix(name, "architect_") {
		return "architect"
	}
	return name
}

// promptCacheBase derives a stable short hash from the book directory as the prompt-cache
// identity prefix: the same book shares routing buckets across process restarts, and no local
// path leaks to the provider. The role suffix is appended by the caller, and each subagent
// spawn appends "#seq" (one key per session).
func promptCacheBase(bookDir string) string {
	sum := sha256.Sum256([]byte(bookDir))
	return "nvl-" + hex.EncodeToString(sum[:6])
}

// subagentMaxRetries is the LLM retry cap for every Worker.
// Backoff: exponential (bounded by maxDelay), honouring a server Retry-After first.
// Tools only start once the full Assistant message is committed, so stream-idle / 503 /
// brief network jitter can be retried safely inside a Worker without replaying tool side
// effects.
const subagentMaxRetries = 7

// UsageRecorder is BuildWorkers' optional usage callback; its signature matches OnMessage.
// It fires once per agent message, with the Host layer aggregating. task is this spawn's task
// text, used as the session identity so cache-chain break detection can reset its baseline per
// session. nil means no tracking.
type UsageRecorder func(agentName, task string, msg agentcore.AgentMessage)

// ApplyThinking applies a specific role's reasoning effort to its Worker (used by the runtime
// /model adjustment). architect -> both architect_* subagents; writer/editor -> the matching
// subagent. An empty level keeps the model/provider default. Other role names are ignored.
type ApplyThinking func(role string, level agentcore.ThinkingLevel)

// ParseThinkingLevel converts a configuration string into agentcore.ThinkingLevel.
// "" is valid (= do not override / inherit); anything else must be one of
// off/low/medium/high/xhigh/max, otherwise an error is returned (degraded to empty with a
// warning at startup, echoed back to the user at runtime).
func ParseThinkingLevel(s string) (agentcore.ThinkingLevel, error) {
	lv := agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(s))
	switch lv {
	case "", agentcore.ThinkingOff, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax:
		return lv, nil
	default:
		return "", fmt.Errorf("mức suy luận không hợp lệ %q (chọn một trong: off/low/medium/high/xhigh/max)", s)
	}
}

func ResolveThinkingForModel(model agentcore.ChatModel, level agentcore.ThinkingLevel) (agentcore.ThinkingLevel, bool) {
	level = agentcore.NormalizeThinkingLevel(level)
	// For an ordinary chat model that does not support thinking, an explicit off is not a no-op
	// but an illegal parameter.
	if cp, ok := model.(llm.CapabilityProvider); ok && cp.Capabilities().Thinking.Supported == llm.SupportNo {
		return agentcore.ThinkingAuto, level == agentcore.ThinkingAuto
	}
	return llm.ThinkingPolicyFor(model).Resolve(level)
}

func AvailableThinkingForModel(model agentcore.ChatModel) []agentcore.ThinkingLevel {
	if cp, ok := model.(llm.CapabilityProvider); ok && cp.Capabilities().Thinking.Supported == llm.SupportNo {
		return []agentcore.ThinkingLevel{agentcore.ThinkingAuto}
	}
	return llm.ThinkingPolicyFor(model).Available
}

// roleThinking resolves a role's effective reasoning effort, degrading an invalid value to
// empty (no override) with a warning.
func roleThinking(cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	lv, err := ParseThinkingLevel(cfg.ResolveReasoningEffort(role))
	if err != nil {
		slog.Warn("bỏ qua cấu hình mức suy luận không hợp lệ", "module", "agent", "role", role, "err", err)
		return ""
	}
	return lv
}

func resolvedRoleThinking(model agentcore.ChatModel, cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	resolved, _ := ResolveThinkingForModel(model, roleThinking(cfg, role))
	return resolved
}

// BuildWorkers assembles the three Workers (architect_short/long, writer, editor) into a
// programmatically callable subagent.Runner. The Engine calls their typed entry points
// directly, with no LLM tool layer.
// (docs/engine-rfc.md §1)。
// It returns the Runner, the WriterRestorePack and ApplyThinking (so a runtime /model change
// adjusts each role's reasoning effort; the ContextManager for writer/architect/editor is
// rebuilt automatically through its factory).
// onGuardBlock is optional (nil-safe): the audit callback for each Worker StopGuard's block /
// escalation.
func BuildWorkers(
	cfg bootstrap.Config,
	store *store.Store,
	styleStats *tools.StyleStatsIndex,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	recordUsage UsageRecorder,
	onGuardBlock guard.BlockHook,
) (*subagent.Runner, *ctxpack.WriterRestorePack, ApplyThinking) {
	// Shared tools
	contextTool := tools.NewContextTool(store, bundle.References, cfg.Style, styleStats)
	readChapter := tools.NewReadChapterTool(store)

	architectTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveBookTool(store),
		tools.NewSaveFoundationTool(store),
		tools.NewReviseOutlineTool(store),
		// A chapter in the rework queue can only carry "what it should become" through
		// chapter_contract: revise_outline may not touch written chapters, and
		// save_foundation(outline) is forbidden from a full overwrite during the writing period.
		tools.NewPlanChapterTool(store),
		tools.NewResolveOutlineFeedbackTool(store),
		tools.NewAuditFoundationTool(store),
	}
	writerTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewPlanChapterTool(store),
		tools.NewDraftChapterTool(store),
		tools.NewEditChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCommitChapterTool(store, styleStats),
	}
	editorTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewSaveReviewTool(store),
		tools.NewSaveArcSummaryTool(store),
		tools.NewSaveVolumeSummaryTool(store),
	}

	// Provider failover is logged only; the host is not notified.
	reportFailover := func(ev bootstrap.FailoverEvent) {
		slog.Warn("chuyển provider",
			"module", "agent",
			"role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err,
		)
	}

	architectModel := models.ForRoleWithFailover("architect", reportFailover)
	writerModel := models.ForRoleWithFailover("writer", reportFailover)
	editorModel := models.ForRoleWithFailover("editor", reportFailover)

	// The Writer's ContextManager is rebuilt by its factory on every call, so the window follows
	// model swaps dynamically (see the factory below).
	writerProvider, writerModelName, _ := models.CurrentSelection("writer")
	writerContextWindow, writerSource := cfg.ResolveContextWindow(writerProvider, writerModelName)
	bootstrap.LogContextWindowChoice("writer", writerModelName, writerContextWindow, writerSource)

	// modelLookup attaches _meta:{provider,model} to every assistant message when written to a
	// session, so replay no longer depends on the "current ModelSet" to infer historical cost
	// and stays exact even when the model is switched mid-run.
	modelLookup := func(agentName string) (string, string) {
		role := agentToRole(agentName)
		provider, name, _ := models.CurrentSelection(role)
		return provider, name
	}
	baseOnMsg := store.Sessions.SubAgentLogger(modelLookup)
	onMsg := func(agentName, task string, msg agentcore.AgentMessage) {
		baseOnMsg(agentName, task, msg)
		if recordUsage != nil {
			recordUsage(agentName, task, msg)
		}
	}

	// Prompt cache: one base per book, one name per role, one key per session (each subagent
	// spawn appends #seq). OpenAI-family providers use prompt_cache_key for routing affinity;
	// Claude-family providers use cache_control with a rolling breakpoint (a system floor plus
	// the tip of the last message). When a provider does not support it, agentcore drops it
	// silently based on capability; the cache read is always a net win across multi-turn
	// sessions, so there is no switch.
	cacheBase := promptCacheBase(store.Dir())

	architectStopGuardFactory := func(_, _ string) agentcore.StopGuard {
		return guard.NewArchitectStopGuard(store, onGuardBlock)
	}
	architectThinking, _ := ResolveThinkingForModel(architectModel, roleThinking(cfg, "architect"))
	architectShort := subagent.Config{
		Name:             "architect_short",
		Description:      "Quy hoạch truyện ngắn: sinh thiết lập gọn và đại cương phẳng cho câu chuyện một tập, một xung đột, mật độ cao",
		Model:            architectModel,
		SystemPrompt:     bundle.WithRating(bundle.Prompts.ArchitectShort),
		Tools:            architectTools,
		MaxTurns:         15,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    architectThinking,
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-architect_short",
		StopAfterToolResult: func(toolName string, result json.RawMessage) bool {
			return foundationReadyResult(toolName, result)
		},
		StopGuardFactory: architectStopGuardFactory,
	}
	architectLong := subagent.Config{
		Name:                "architect_long",
		Description:         "Quy hoạch truyện dài: sinh thiết lập phân tầng và đại cương tập/cung cho truyện dài kỳ, nâng cấp liên tục",
		Model:               architectModel,
		SystemPrompt:        bundle.WithRating(bundle.Prompts.ArchitectLong),
		Tools:               architectTools,
		MaxTurns:            20,
		MaxRetries:          subagentMaxRetries,
		ThinkingLevel:       architectThinking,
		OnMessage:           onMsg,
		CacheLastMessage:    "ephemeral",
		PromptCacheKey:      cacheBase + "-architect_long",
		StopAfterToolResult: architectLongShouldStopAfterToolResult,
		StopGuardFactory:    architectStopGuardFactory,
	}

	// The single assembly path: the protocol template has its {{VOICE}} placeholder filled in
	// place with the voice section, then the style preset is appended.
	// The eval voice A/B uses the same function, keeping both arms equivalent
	// (docs/voice-layer.md §3.2).
	writerPrompt := bundle.WithRating(assets.BuildWriterPrompt(bundle.Prompts.Writer, bundle.Voice, bundle.Styles[cfg.Style]))

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(store)

	writer := subagent.Config{
		Name:             "writer",
		Description:      "Sáng tác: tự chủ hoàn thành một chương gồm lên ý tưởng, viết, tự thẩm duyệt và commit",
		Model:            writerModel,
		SystemPrompt:     writerPrompt,
		Tools:            writerTools,
		MaxTurns:         30,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    resolvedRoleThinking(writerModel, cfg, "writer"),
		StopAfterTools:   []string{"commit_chapter"},
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-writer",
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return guard.NewWriterStopGuard(store, onGuardBlock)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			// Rebuild the context manager per chapter against the current writer model.
			window, _ := models.ResolveContextWindow(bootstrap.ModelProvider(model), bootstrap.ModelName(model))
			return newContextManager(contextManagerConfig{
				Model:         model,
				ContextWindow: window,
				ReserveTokens: bootstrap.CompactReserveTokens(window),
				Agent:         "writer",
				// Commit projection, so later rounds do not keep rewriting the request prefix.
				CommitProjected: true,
				ToolMicrocompact: &corecontext.ToolResultMicrocompactConfig{
					MinResultTokens: 200,
				},
				ExtraStrategies: []corecontext.Strategy{
					ctxpack.NewStoreSummaryCompact(ctxpack.StoreSummaryCompactConfig{
						Store:            store,
						KeepRecentTokens: 20000,
					}),
				},
				Summary: &corecontext.FullSummaryConfig{
					PostSummaryHooks:    []corecontext.PostSummaryHook{restore.Hook()},
					SystemPrompt:        ctxpack.WriterSummarySystemPrompt,
					SummaryPrompt:       ctxpack.WriterSummaryPrompt,
					UpdateSummaryPrompt: ctxpack.WriterUpdateSummaryPrompt,
					TurnPrefixPrompt:    ctxpack.WriterTurnPrefixPrompt,
				},
			})
		},
	}

	editor := subagent.Config{
		Name:             "editor",
		Description:      "Người thẩm duyệt: đọc nguyên văn, phát hiện vấn đề ở hai tầng cấu trúc và thẩm mỹ",
		Model:            editorModel,
		SystemPrompt:     bundle.WithRating(bundle.Prompts.Editor),
		Tools:            editorTools,
		MaxTurns:         20,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    resolvedRoleThinking(editorModel, cfg, "editor"),
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-editor",
		// Stop as soon as a terminal artefact is hit. A terminal exit still consults the
		// StopGuard (contract test TestContract_TerminalToolExitConsultsStopGuard), and the
		// task-aware NewEditorStopGuard vetoes an early exit where a summary was assigned but
		// only a review was done, so save_review may hard-stop safely.
		StopAfterToolResult: func(toolName string, _ json.RawMessage) bool {
			return toolName == "save_review" || toolName == "save_arc_summary" || toolName == "save_volume_summary"
		},
		StopGuardFactory: func(_, task string) agentcore.StopGuard {
			return guard.NewEditorStopGuard(store, task, onGuardBlock)
		},
	}

	runner := subagent.NewRunner(architectShort, architectLong, writer, editor)

	// Wire each role's reasoning effort at runtime (used by /model adjustments).
	applyThinking := func(role string, level agentcore.ThinkingLevel) {
		switch role {
		case "architect":
			level, _ = ResolveThinkingForModel(models.ForRole("architect"), level)
			runner.SetThinkingLevel("architect_short", level)
			runner.SetThinkingLevel("architect_long", level)
		case "writer", "editor":
			level, _ = ResolveThinkingForModel(models.ForRole(role), level)
			runner.SetThinkingLevel(role, level)
		}
	}

	return runner, restore, applyThinking
}

type saveFoundationResult struct {
	Type            string `json:"type"`
	FoundationReady bool   `json:"foundation_ready"`
}

func decodeSaveFoundationResult(toolName string, result json.RawMessage) saveFoundationResult {
	if toolName != "save_foundation" {
		return saveFoundationResult{}
	}
	var r saveFoundationResult
	_ = json.Unmarshal(result, &r)
	return r
}

func architectLongShouldStopAfterToolResult(toolName string, result json.RawMessage) bool {
	if foundationReadyResult(toolName, result) {
		return true
	}
	r := decodeSaveFoundationResult(toolName, result)
	switch r.Type {
	case "expand_arc", "complete_book":
		return true
	default:
		return false
	}
}

func foundationReadyResult(toolName string, result json.RawMessage) bool {
	if toolName != "audit_foundation" {
		return false
	}
	var r struct {
		FoundationReady bool `json:"foundation_ready"`
	}
	return json.Unmarshal(result, &r) == nil && r.FoundationReady
}
