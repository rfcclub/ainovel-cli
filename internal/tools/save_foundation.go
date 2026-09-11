package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveFoundationTool saves the foundation (premise/outline/characters) and is for the Architect only.
type SaveFoundationTool struct {
	store *store.Store
}

func NewSaveFoundationTool(store *store.Store) *SaveFoundationTool {
	return &SaveFoundationTool{store: store}
}

func (t *SaveFoundationTool) Name() string { return "save_foundation" }
func (t *SaveFoundationTool) Description() string {
	return "Lưu thiết lập nền tảng của tiểu thuyết (premise/outline/characters/world_rules/compass...). **Đây là cửa ngõ lưu trữ duy nhất**: nội dung chưa qua công cụ này sẽ không vào store, chỉ xuất Markdown/JSON trong tin nhắn thì coi như mất. Tham số cố định là {type, content, scale?, volume?, arc?}. type nhận một trong premise / outline / layered_outline / characters / world_rules / expand_arc / append_volume / update_compass / complete_book. Với premise, content phải là chuỗi Markdown; các type khác thì content ưu tiên truyền thẳng mảng hoặc object JSON. expand_arc hiệu chỉnh và mở rộng một cung khung xương chưa viết (số chương chi tiết trong một cung không được quá 8 chương — thẩm duyệt cuối cung cần đọc trọn cung một lần, quá dài không thể duyệt; vượt giới hạn hãy tách thành nhiều cung, cần volume + arc, content là {title, goal, chapters}, có thể sửa mục tiêu khung xương ban đầu dựa trên chính văn đã viết); append_volume thêm tập mới (content là JSON VolumeOutline đầy đủ, gồm cấu trúc cung; mang \"final\": true ở tầng trên cùng là tuyên bố tập kết thúc — toàn sách khép lại ở tập đó, viết xong mọi chương thì tự động kết thúc, không cần gọi complete_book nữa); update_compass cập nhật hướng kết cục (content là JSON StoryCompass, trường chỉ gồm {ending_direction: string bắt buộc, open_threads?: string[], estimated_scale?: string}, mọi trường khác đều không được chấp nhận); complete_book tuyên bố toàn sách kết thúc (content truyền object rỗng {}, đẩy thẳng Phase=Complete; công cụ sẽ kiểm tra: đại cương đã viết hết chương, không còn hàng đợi làm lại, compass không còn open_threads chưa thu hồi — muốn xác nhận đã thu hồi tuyến dài thì phải update_compass dọn sạch open_threads xuống đĩa trước, muốn kết thúc sớm thì dùng tập kết thúc qua final của append_volume). append_volume / complete_book bắt buộc phải kèm tham số reason (một câu lý do phán định, đối chiếu danh sách tiêu chí kết thúc, ghi vào audit phán định). scale là tùy chọn, chỉ nhận short / mid / long."
}
func (t *SaveFoundationTool) Label() string { return "Lưu thiết lập" }

// Write tool (cross-domain updates to Outline/Progress/Characters); concurrency is forbidden.
func (t *SaveFoundationTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveFoundationTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveFoundationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("type", schema.Enum("Loại thiết lập", "premise", "outline", "layered_outline", "characters", "world_rules", "expand_arc", "append_volume", "update_compass", "complete_book")).Required(),
		schema.Property("content", map[string]any{
			"description": "Nội dung. premise truyền chuỗi Markdown; các loại khác truyền thẳng mảng hoặc object JSON cũng được, vẫn tương thích nếu truyền chuỗi JSON. Khi expand_arc thì truyền {title, goal, chapters}, title/goal là quy hoạch cung mục tiêu đã hiệu chỉnh theo các sự thật đã hoàn thành.",
		}).Required(),
		schema.Property("scale", schema.Enum("Cấp quy hoạch", "short", "mid", "long")),
		schema.Property("volume", schema.Int("Số thứ tự tập mục tiêu, tính từ 1 (chỉ bắt buộc khi expand_arc)")),
		schema.Property("arc", schema.Int("Số thứ tự cung mục tiêu trong tập, tính từ 1 (chỉ bắt buộc khi expand_arc)")),
		schema.Property("reason", schema.String("Lý do phán định cuối tập (bắt buộc khi append_volume / complete_book): đối chiếu danh sách tiêu chí kết thúc, một câu giải thích vì sao tiếp tập, tuyên bố tập kết thúc hay kết thúc toàn sách")),
	)
}

func (t *SaveFoundationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Scale   string          `json:"scale"`
		Volume  int             `json:"volume"`
		Arc     int             `json:"arc"`
		Reason  string          `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	content, err := normalizeFoundationContent(a.Content)
	if err != nil {
		return nil, err
	}
	if a.Scale != "" {
		switch domain.PlanningTier(a.Scale) {
		case domain.PlanningTierShort, domain.PlanningTierMid, domain.PlanningTierLong:
		default:
			return nil, fmt.Errorf("invalid scale %q, expected short/mid/long: %w", a.Scale, errs.ErrToolArgs)
		}
	}

	result := map[string]any{"saved": true, "type": a.Type, "scale": a.Scale}

	// A full outline belongs to the planning phase alone. During writing only the protected incremental operations may
	// be used, and a completed book must be reopened first; otherwise the completed-chapter protection is bypassed and
	// Progress loses consistency with the chapter facts.
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("check foundation phase: %w: %w", errs.ErrStoreRead, err)
	}
	if (a.Type == "outline" || a.Type == "layered_outline") && progress != nil {
		switch progress.Phase {
		case domain.PhaseWriting:
			return nil, fmt.Errorf(
				"Ở giai đoạn viết không được dùng %s để ghi đè toàn bộ đại cương. Hãy dùng revise_outline để tu chỉnh các chương chưa diễn ra, expand_arc để mở rộng cung khung xương, hoặc append_volume để thêm tập mới: %w",
				a.Type, errs.ErrToolPrecondition)
		case domain.PhaseComplete:
			return nil, fmt.Errorf(
				"Toàn sách đã kết thúc, không được dùng %s ghi đè toàn bộ đại cương. Hãy mở lại tác phẩm trước, rồi dùng các thao tác tu chỉnh đại cương hoặc viết tiếp được bảo vệ: %w",
				a.Type, errs.ErrToolPrecondition)
		}
	}
	if a.Scale != "" {
		if err := t.store.RunMeta.SetPlanningTier(domain.PlanningTier(a.Scale)); err != nil {
			return nil, fmt.Errorf("save planning tier: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// The volume-end choice (continue the volume / close / complete) is the book's heaviest semantic judgement, so its
	// reason must become an audit fact (decisions.jsonl, the same stream as plan_start/intervention); otherwise a
	// premature closing or a misjudged continuation could only be debugged by trawling session logs. The fact snapshot
	// is taken at the moment of the verdict (before the change lands).
	volumeEnd := a.Type == "append_volume" || a.Type == "complete_book"
	if volumeEnd && strings.TrimSpace(a.Reason) == "" {
		return nil, fmt.Errorf("%s bắt buộc phải kèm tham số reason: đối chiếu danh sách tiêu chí kết thúc, một câu giải thích lần này vì sao tiếp tập, tuyên bố tập kết thúc hay kết thúc toàn sách: %w", a.Type, errs.ErrToolArgs)
	}
	var volumeEndFacts json.RawMessage
	if volumeEnd {
		p, err := t.store.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress for volume-end facts: %w: %w", errs.ErrStoreRead, err)
		}
		if p != nil {
			facts := map[string]any{"completed_chapters": len(p.CompletedChapters)}
			if p.Layered {
				outline, outlineErr := t.store.Outline.LoadOutline()
				if outlineErr != nil {
					return nil, fmt.Errorf("load outlined chapters for volume-end facts: %w: %w", errs.ErrStoreRead, outlineErr)
				}
				facts["dynamic_planning"] = true
				facts["outlined_chapters"] = len(outline)
			} else {
				facts["total_chapters"] = p.TotalChapters
			}
			volumeEndFacts, err = json.Marshal(facts)
			if err != nil {
				return nil, fmt.Errorf("marshal volume-end facts: %w", err)
			}
		}
	}

	decode := func(typeName string, out any) error {
		return decodeFoundationJSON(typeName, content, out)
	}

	switch a.Type {
	case "premise":
		if err := t.store.Outline.SavePremise(content); err != nil {
			return nil, fmt.Errorf("save premise: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.AdvancePhase(domain.PhasePremise); err != nil {
			return nil, fmt.Errorf("update premise phase: %w: %w", errs.ErrStoreWrite, err)
		}

	case "outline":
		var entries []domain.OutlineEntry
		if err := decode("outline", &entries); err != nil {
			return nil, err
		}
		if defect := domain.StalledOutline(entries); defect != "" {
			return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
		}
		if err := t.store.Outline.SaveOutline(entries); err != nil {
			return nil, fmt.Errorf("save outline: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.AdvancePhase(domain.PhaseOutline); err != nil {
			return nil, fmt.Errorf("update outline phase: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.SetTotalChapters(len(entries)); err != nil {
			return nil, fmt.Errorf("set total chapters: %w: %w", errs.ErrStoreWrite, err)
		}
		if domain.PlanningTier(a.Scale) != domain.PlanningTierLong {
			if err := t.store.Progress.SetLayered(false); err != nil {
				return nil, fmt.Errorf("disable layered mode: %w: %w", errs.ErrStoreWrite, err)
			}
			if err := t.store.Progress.UpdateVolumeArc(0, 0); err != nil {
				return nil, fmt.Errorf("reset volume/arc: %w: %w", errs.ErrStoreWrite, err)
			}
			if err := t.store.Outline.ClearLayeredOutline(); err != nil {
				return nil, fmt.Errorf("clear layered outline: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		result["chapters"] = len(entries)

	case "layered_outline":
		var volumes []domain.VolumeOutline
		if err := decode("layered_outline", &volumes); err != nil {
			return nil, err
		}
		for vi := range volumes {
			for ai := range volumes[vi].Arcs {
				arc := &volumes[vi].Arcs[ai]
				if defect := domain.OversizedArc(
					fmt.Sprintf("tập %d cung %d", volumes[vi].Index, arc.Index), arcPlannedSize(arc)); defect != "" {
					return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
				}
			}
		}
		if defect := domain.StalledOutline(domain.FlattenOutline(volumes)); defect != "" {
			return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
		}
		if err := t.store.Outline.SaveLayeredOutline(volumes); err != nil {
			return nil, fmt.Errorf("save layered_outline: %w: %w", errs.ErrStoreWrite, err)
		}
		total := domain.EstimatedChapterCapacity(volumes)
		if err := t.store.Progress.AdvancePhase(domain.PhaseOutline); err != nil {
			return nil, fmt.Errorf("update outline phase: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.SetTotalChapters(total); err != nil {
			return nil, fmt.Errorf("set total chapters: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.SetLayered(true); err != nil {
			return nil, fmt.Errorf("enable layered mode: %w: %w", errs.ErrStoreWrite, err)
		}
		if len(volumes) > 0 && len(volumes[0].Arcs) > 0 {
			if err := t.store.Progress.UpdateVolumeArc(volumes[0].Index, volumes[0].Arcs[0].Index); err != nil {
				return nil, fmt.Errorf("set initial volume/arc: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		result["volumes"] = len(volumes)
		result["dynamic_planning"] = true
		result["outlined_chapters"] = len(domain.FlattenOutline(volumes))

	case "characters":
		var chars []domain.Character
		if err := decode("characters", &chars); err != nil {
			return nil, err
		}
		if err := t.store.Characters.Save(chars); err != nil {
			return nil, fmt.Errorf("save characters: %w: %w", errs.ErrStoreWrite, err)
		}
		result["count"] = len(chars)

	case "world_rules":
		var rules []domain.WorldRule
		if err := decode("world_rules", &rules); err != nil {
			return nil, err
		}
		if err := t.store.World.SaveWorldRules(rules); err != nil {
			return nil, fmt.Errorf("save world_rules: %w: %w", errs.ErrStoreWrite, err)
		}
		result["count"] = len(rules)

	case "expand_arc":
		if a.Volume <= 0 || a.Arc <= 0 {
			return nil, fmt.Errorf(
				"expand_arc cần volume và arc (số tập/số cung đều tính từ 1, nhưng nhận được volume=%d arc=%d); "+
					"hãy gọi novel_context trước, dùng giá trị index của cung mục tiêu trong layered_outline để điền vào: %w",
				a.Volume, a.Arc, errs.ErrToolArgs)
		}
		var expansion domain.ArcExpansion
		if err := decode("expand_arc", &expansion); err != nil {
			return nil, err
		}
		if defect := domain.OversizedArc(
			fmt.Sprintf("tập %d cung %d", a.Volume, a.Arc), len(expansion.Chapters)); defect != "" {
			return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
		}
		if err := t.store.ExpandArc(a.Volume, a.Arc, expansion); err != nil {
			return nil, fmt.Errorf("expand arc: %w: %w", errs.ErrStoreWrite, err)
		}
		result["volume"] = a.Volume
		result["arc"] = a.Arc
		result["title"] = expansion.Title
		result["goal"] = expansion.Goal
		result["chapters"] = len(expansion.Chapters)
		if err := t.consumeWriterFeedback(); err != nil {
			return nil, err
		}

	case "append_volume":
		p, err := t.store.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
		}
		if p != nil && p.Phase == domain.PhaseComplete {
			return nil, fmt.Errorf("toàn sách đã kết thúc (phase=complete), không cho phép thêm tập mới: %w", errs.ErrToolPrecondition)
		}
		var vol domain.VolumeOutline
		if err := decode("append_volume", &vol); err != nil {
			return nil, err
		}
		for i := range vol.Arcs {
			arc := &vol.Arcs[i]
			if defect := domain.OversizedArc(
				fmt.Sprintf("cung %d của tập mới", arc.Index), arcPlannedSize(arc)); defect != "" {
				return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
			}
		}
		prior, err := t.store.Outline.LoadLayeredOutline()
		if err != nil {
			return nil, fmt.Errorf("load layered outline: %w: %w", errs.ErrStoreRead, err)
		}
		if err := t.store.AppendVolume(vol); err != nil {
			return nil, fmt.Errorf("append volume: %w: %w", errs.ErrStoreWrite, err)
		}
		result["volume"] = vol.Index
		if vol.Final {
			result["final_volume"] = true
		} else if domain.FinaleVolume(prior) > 0 {
			// Fact echo: the previously declared closing state is lifted by appending an ordinary new volume (which becomes the last)
			result["finale_released"] = true
		}
		result["arcs"] = len(vol.Arcs)
		chCount := 0
		for _, arc := range vol.Arcs {
			chCount += len(arc.Chapters)
		}
		if chCount > 0 {
			result["chapters"] = chCount
		}
		if err := t.consumeWriterFeedback(); err != nil {
			return nil, err
		}

	case "complete_book":
		// The one entry point for completing the whole book: push Phase=Complete directly.
		// It is allowed only during the Writing phase, preventing a stray call in the planning phase from skipping the
		// entire book. It refuses when a rework queue exists — PendingRewrites must drain before the book can end.
		progress, perr := t.store.Progress.Load()
		if perr != nil {
			return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, perr)
		}
		if progress == nil {
			return nil, fmt.Errorf("progress chưa được khởi tạo: %w", errs.ErrToolPrecondition)
		}
		if progress.Phase != domain.PhaseWriting {
			return nil, fmt.Errorf("complete_book chỉ gọi được ở giai đoạn writing (hiện tại phase=%s): %w", progress.Phase, errs.ErrToolPrecondition)
		}
		if len(progress.PendingRewrites) > 0 {
			return nil, fmt.Errorf("còn %d chương trong hàng đợi làm lại, xử lý xong rồi mới gọi complete_book: %w", len(progress.PendingRewrites), errs.ErrToolPrecondition)
		}
		// Every enumerable completion precondition must sit in code (the trichotomy) rather than relying on the prompt's
		// "completion checklist" — a real incident: right after planning landed and phase flipped to writing, a weak model
		// casually mis-called complete_book and marked the book complete at 0/68 chapters.
		if len(progress.CompletedChapters) == 0 {
			return nil, fmt.Errorf("chưa viết chương nào thì không thể kết thúc sách; sau khi quy hoạch xong, việc viết do hệ thống tự đẩy, không cần gọi complete_book: %w", errs.ErrToolPrecondition)
		}
		next := progress.NextChapter()
		if progress.Layered {
			outline, outlineErr := t.store.Outline.LoadOutline()
			if outlineErr != nil {
				return nil, fmt.Errorf("load outlined chapters: %w: %w", errs.ErrStoreRead, outlineErr)
			}
			if next <= len(outline) {
				return nil, fmt.Errorf("đại cương chi tiết hiện tại còn chương chưa viết (chương kế tiếp %d/đã chi tiết hóa %d), không thể kết thúc sách; muốn kết thúc sớm hãy dùng append_volume với tầng trên cùng của JSON tập mang \"final\": true để tuyên bố tập kết thúc: %w", next, len(outline), errs.ErrToolPrecondition)
			}
			// The flat outline holds only expanded arcs, leaving skeleton arcs entirely invisible to the comparison above;
			// without a separate check, a book could be declared complete with a whole volume unexpanded.
			volumes, volErr := t.store.Outline.LoadLayeredOutline()
			if volErr != nil {
				return nil, fmt.Errorf("load layered outline: %w: %w", errs.ErrStoreRead, volErr)
			}
			if skeletons := domain.SkeletonArcs(volumes); len(skeletons) > 0 {
				return nil, fmt.Errorf("còn %d cung khung xương chưa mở rộng (ví dụ: %s), không thể kết thúc sách; "+
					"hãy expand_arc để mở rộng và viết hết, hoặc dùng append_volume mang \"final\": true để tuyên bố tập kết thúc: %w",
					len(skeletons), skeletons[0], errs.ErrToolPrecondition)
			}
		} else if progress.TotalChapters > 0 && next <= progress.TotalChapters {
			return nil, fmt.Errorf("đại cương còn chương chưa viết (chương kế tiếp %d/tổng %d), không thể kết thúc sách; muốn kết thúc sớm hãy dùng append_volume với tầng trên cùng của JSON tập mang \"final\": true để tuyên bố tập kết thúc: %w", next, progress.TotalChapters, errs.ErrToolPrecondition)
		}
		// An active long thread left untied blocks completion — OpenThreads' field contract is exactly "must be tied off
		// before an ending". This is not a semantic re-judgement: if the model truly believes everything is tied off, it
		// first clears open_threads with update_compass and then completes, turning "an exemption in the prose" into an
		// auditable persisted action (measured: when continuing an imported completed book, the architect cited chapter and
		// verse to bypass completion-checklist item 3 and complete directly, locking the user's continuation request out
		// via the completion rules).
		compass, err := t.store.Outline.LoadCompass()
		if err != nil {
			return nil, fmt.Errorf("load compass: %w: %w", errs.ErrStoreRead, err)
		}
		if compass != nil && len(compass.OpenThreads) > 0 {
			return nil, fmt.Errorf("compass còn %d tuyến dài đang mở chưa thu hồi (ví dụ: %s), không thể kết thúc sách. Nếu xác nhận đã thu hồi hết hãy update_compass để dọn sạch open_threads rồi gọi complete_book; nếu vẫn cần mở rộng thì dùng append_volume (có thể mang \"final\": true để tuyên bố tập kết thúc): %w",
				len(compass.OpenThreads), compass.OpenThreads[0], errs.ErrToolPrecondition)
		}
		if err := t.store.Progress.MarkComplete(); err != nil {
			return nil, fmt.Errorf("mark complete: %w: %w", errs.ErrStoreWrite, err)
		}
		result["book_complete"] = true
		result["phase"] = string(domain.PhaseComplete)

	case "update_compass":
		var compass domain.StoryCompass
		if err := decode("compass", &compass); err != nil {
			return nil, err
		}
		// The tool layer forcibly overwrites LastUpdated with the current completed-chapter count and does not trust the
		// LLM's own value. The LLM usually forgets it or leaves 0, which makes diag.CompassDrift report falsely and
		// distorts Router routing.
		p, err := t.store.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
		}
		if p != nil {
			compass.LastUpdated = p.LatestCompleted()
		}
		if err := t.store.Outline.SaveCompass(compass); err != nil {
			return nil, fmt.Errorf("save compass: %w: %w", errs.ErrStoreWrite, err)
		}
		result["ending_direction"] = compass.EndingDirection
		result["last_updated"] = compass.LastUpdated
		if err := t.consumeWriterFeedback(); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unknown type %q, expected premise/outline/layered_outline/characters/world_rules/expand_arc/append_volume/update_compass/complete_book: %w", a.Type, errs.ErrToolArgs)
	}

	// checkpoint
	scope := domain.GlobalScope()
	if a.Type == "expand_arc" {
		scope = domain.ArcScope(a.Volume, a.Arc)
	} else if a.Type == "append_volume" {
		scope = domain.GlobalScope()
	}
	if _, err := t.store.Checkpoints.AppendArtifact(scope, a.Type, foundationArtifact(a.Type)); err != nil {
		return nil, fmt.Errorf("checkpoint foundation %s: %w: %w", a.Type, errs.ErrStoreWrite, err)
	}

	if volumeEnd {
		t.recordVolumeEndDecision(a.Type, a.Reason, volumeEndFacts, result)
	}

	// Returns the remaining unfinished items. foundation_audit remains after all initial artifacts are present, and
	// writing is allowed only once audit_foundation returns ready=true for the version actually on disk.
	remaining, err := t.store.FoundationMissing()
	if err != nil {
		return nil, fmt.Errorf("load foundation state: %w: %w", errs.ErrStoreRead, err)
	}
	ready := len(remaining) == 0
	result["remaining"] = remaining
	result["foundation_ready"] = ready
	return json.Marshal(result)
}

func foundationArtifact(t string) string {
	switch t {
	case "premise":
		return "premise.md"
	case "outline":
		return "outline.json"
	case "layered_outline", "expand_arc", "append_volume":
		return "layered_outline.json"
	case "complete_book":
		return "meta/progress.json"
	case "characters":
		return "characters.json"
	case "world_rules":
		return "world_rules.json"
	case "update_compass":
		return "meta/compass.json"
	default:
		return ""
	}
}

// decodeFoundationJSON parses save_foundation's content field, attaching the line and column plus the most common fix
// hint on failure, so the LLM can locate the problem directly on its next retry instead of guessing.
func decodeFoundationJSON(typeName, content string, out any) error {
	err := json.Unmarshal([]byte(content), out)
	if err == nil {
		return nil
	}
	hint := `Nguyên nhân thường gặp: dấu ngoặc kép trong giá trị chuỗi chưa escape thành \", ký tự xuống dòng chưa escape thành \n, hoặc thiếu dấu phẩy giữa các trường của object. Hãy sinh lại toàn bộ đoạn đó một lần.`
	if se, ok := err.(*json.SyntaxError); ok {
		line, col := offsetToLineCol(content, int(se.Offset))
		return fmt.Errorf("parse %s JSON (line %d col %d): %w — %s", typeName, line, col, err, hint)
	}
	return fmt.Errorf("parse %s JSON: %w — %s", typeName, err, hint)
}

func offsetToLineCol(s string, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(s) {
		offset = len(s)
	}
	line, col := 1, 1
	for i := 0; i < offset; i++ {
		if s[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func normalizeFoundationContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("content is required: %w", errs.ErrToolArgs)
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	if !json.Valid(raw) {
		return "", fmt.Errorf("invalid content: expected Markdown string or valid JSON value: %w", errs.ErrToolArgs)
	}
	return string(raw), nil
}

// recordVolumeEndDecision writes the reason for the volume-end choice (continue the volume / close / complete) into
// the decision audit.
// Best-effort: the structural change has already landed, so an audit failure only warns and never rolls back — erroring
// would make the model retry an operation that already succeeded (appending a duplicate volume).
func (t *SaveFoundationTool) recordVolumeEndDecision(action, reason string, facts json.RawMessage, result map[string]any) {
	decision := map[string]any{"action": action}
	if v, ok := result["volume"]; ok {
		decision["volume"] = v
	}
	if _, ok := result["final_volume"]; ok {
		decision["final"] = true
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		slog.Error("tuần tự hóa phán định cuối tập thất bại", "module", "tools", "action", action, "err", err)
		return
	}
	if _, err := t.store.Decisions.Append(store.DecisionRecord{
		Kind:     "volume_end",
		Decider:  "architect",
		Facts:    facts,
		Decision: raw,
		Reason:   reason,
	}); err != nil {
		slog.Error("ghi audit phán định cuối tập thất bại", "module", "tools", "action", action, "err", err)
	}
}

// consumeWriterFeedback clears handled planning feedback after a structural operation succeeds.
func (t *SaveFoundationTool) consumeWriterFeedback() error {
	if err := t.store.Outline.ClearOutlineFeedback(); err != nil {
		return fmt.Errorf("clear outline feedback: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}

// arcPlannedSize takes an arc's actual scale: the detailed chapter count when expanded, or the estimated count while still a skeleton.
func arcPlannedSize(arc *domain.ArcOutline) int {
	if n := len(arc.Chapters); n > 0 {
		return n
	}
	return arc.EstimatedChapters
}
