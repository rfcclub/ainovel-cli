package tools

import (
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
)

type contextBuildState struct {
	chapter         int
	profile         domain.ContextProfile
	progress        *domain.Progress
	runMeta         *domain.RunMeta
	outline         []domain.OutlineEntry
	currentEntry    *domain.OutlineEntry
	chapterPlan     *domain.ChapterPlan
	storyThreads    []domain.RecallItem
	foreshadow      []domain.ForeshadowEntry
	relationships   []domain.RelationshipEntry
	allStateChanges []domain.StateChange
	styleRules      *domain.WritingStyleRules
}

type chapterContextEnvelope struct {
	Working    map[string]any
	Episodic   map[string]any
	References map[string]any
	Selected   map[string]any
}

type architectContextEnvelope struct {
	Planning   map[string]any
	Foundation map[string]any
	References map[string]any
}

// planningVolumeOutline is the Architect's read-only structural projection. Completed arcs keep only their bounds
// and counts while unfinished or not-yet-reached arcs keep detailed chapters, so a long book's context does not grow
// linearly with the number of chapters written.
type planningVolumeOutline struct {
	Index int                  `json:"index"`
	Title string               `json:"title"`
	Theme string               `json:"theme"`
	Final bool                 `json:"final,omitempty"`
	Arcs  []planningArcOutline `json:"arcs"`
}

type planningArcOutline struct {
	Index             int                   `json:"index"`
	Title             string                `json:"title"`
	Goal              string                `json:"goal"`
	Status            string                `json:"status"`
	StartChapter      int                   `json:"start_chapter,omitempty"`
	EndChapter        int                   `json:"end_chapter,omitempty"`
	ChapterCount      int                   `json:"chapter_count,omitempty"`
	EstimatedChapters int                   `json:"estimated_chapters,omitempty"`
	Chapters          []domain.OutlineEntry `json:"chapters,omitempty"`
}

func newChapterContextEnvelope() chapterContextEnvelope {
	return chapterContextEnvelope{
		Working:    make(map[string]any),
		Episodic:   make(map[string]any),
		References: make(map[string]any),
		Selected:   make(map[string]any),
	}
}

func newArchitectContextEnvelope() architectContextEnvelope {
	return architectContextEnvelope{
		Planning:   make(map[string]any),
		Foundation: make(map[string]any),
		References: make(map[string]any),
	}
}

func (e chapterContextEnvelope) apply(result map[string]any) {
	// The chapter path applies the prepare stage and the build stage in turn, so existing sections are merged.
	mergeEnvelopeSection(result, "working_memory", e.Working)
	mergeEnvelopeSection(result, "episodic_memory", e.Episodic)
	mergeEnvelopeSection(result, "reference_pack", e.References)
	if len(e.Selected) > 0 {
		mergeEnvelopeSection(result, "selected_memory", e.Selected)
	}
}

// mergeEnvelopeSection merges section into the existing container at result[key], mounting it directly when no container exists.
func mergeEnvelopeSection(result map[string]any, key string, section map[string]any) {
	if existing, ok := result[key].(map[string]any); ok {
		for k, v := range section {
			existing[k] = v
		}
		return
	}
	result[key] = section
}

func (e architectContextEnvelope) apply(result map[string]any) {
	result["planning_memory"] = e.Planning
	result["foundation_memory"] = e.Foundation
	result["reference_pack"] = e.References
}

// buildProgressStatus returns a progress summary when the Architect passes no chapter.
// The Writer/Editor chapter path does not need this information, and including it would disturb the writing.
func (t *ContextTool) buildProgressStatus(result map[string]any, reads *contextReads) {
	progress, err := t.store.Progress.Load()
	if err != nil {
		reads.require("progress_status", err)
		return
	}
	if progress == nil {
		return
	}
	status := map[string]any{
		"phase":              string(progress.Phase),
		"flow":               string(progress.Flow),
		"completed_chapters": len(progress.CompletedChapters),
		"next_chapter":       progress.NextChapter(),
		"total_word_count":   progress.TotalWordCount,
	}
	if progress.InProgressChapter > 0 {
		status["in_progress_chapter"] = progress.InProgressChapter
	}
	if len(progress.PendingRewrites) > 0 {
		status["pending_rewrites"] = progress.PendingRewrites
		status["rewrite_reason"] = progress.RewriteReason
	}
	if progress.Layered {
		status["layered"] = true
		status["dynamic_planning"] = true
		outline, outlineErr := t.store.Outline.LoadOutline()
		if outlineErr != nil {
			reads.require("progress_status.outline", outlineErr)
		} else {
			status["outlined_chapters"] = len(outline)
		}
		status["current_volume"] = progress.CurrentVolume
		status["current_arc"] = progress.CurrentArc
	} else {
		status["total_chapters"] = progress.TotalChapters
	}
	if progress.Phase == domain.PhaseComplete {
		status["finished"] = true
	}
	result["progress_status"] = status
}

// buildUserRules injects the merged Bundle into working_memory.user_rules (the canonical path).
//
// Single-point injection: whichever path calls novel_context — writer, editor or architect — gets consistent
// preferences at working_memory.user_rules. The architect path had no working_memory, so this function creates one on
// demand (holding only user_rules); on a chapter > 0 path working_memory already exists and it is embedded directly.
//
// It is injected even when the Bundle is empty, keeping the field stable so the LLM does not see user_rules=null and take an abnormal branch.
//
// Injection policy: the LLM sees structured + preferences only — those two are the preferences to follow while
// writing. sources / conflicts are diagnostics (for investigating user conflicts) and do not go to the LLM; the CLI's
// startup diagnostics panel shows them on demand.
func (t *ContextTool) buildUserRules(result map[string]any, reads *contextReads) {
	snap, err := t.store.UserRules.Load()
	if err != nil {
		reads.require("user_rules", err)
	}
	if snap == nil {
		// When the snapshot is not initialised yet, fall back to the built-in default so the
		// mechanical floor (word count / forbidden phrases / fatigue words) always exists.
		def := rules.BuildSnapshot([]rules.Candidate{rules.SystemDefaults()})
		snap = &def
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		working = map[string]any{}
		result["working_memory"] = working
	}
	working["user_rules"] = snap.Payload()
}

func (t *ContextTool) buildSimulationProfile(result map[string]any, sectionKey string, reads *contextReads) {
	profile, err := t.store.Simulation.Load()
	if err != nil {
		reads.warn("simulation_profile", err)
		return
	}
	compact := domain.CompactSimulationProfile(profile)
	if compact == nil {
		return
	}
	section, ok := result[sectionKey].(map[string]any)
	if !ok {
		section = map[string]any{}
		result[sectionKey] = section
	}
	section["simulation_profile"] = compact
}

func (t *ContextTool) buildBaseContext(result map[string]any, reads *contextReads) {
	if book, err := t.store.Book.Load(); err == nil && book != nil {
		result["book"] = book
	} else {
		reads.require("book", err)
	}
	if premise, err := t.store.Outline.LoadPremise(); err == nil && premise != "" {
		result["premise"] = premise
		if sections := parsePremiseSections(premise); len(sections) > 0 {
			result["premise_sections"] = sections
		}
		tier := domain.PlanningTier("")
		if meta, err := t.store.RunMeta.Load(); err == nil && meta != nil {
			tier = meta.PlanningTier
		} else {
			reads.require("run_meta", err)
		}
		result["premise_structure"] = premiseStructure(premise, tier)
	} else {
		reads.require("premise", err)
	}
	if rules, err := t.store.World.LoadWorldRules(); err == nil && len(rules) > 0 {
		result["world_rules"] = rules
	} else {
		reads.require("world_rules", err)
	}
}

func (t *ContextTool) prepareChapterContext(chapter int, envelope *chapterContextEnvelope, reads *contextReads) contextBuildState {
	state := contextBuildState{
		chapter: chapter,
		profile: domain.NewContextProfile(0),
	}

	progress, err := t.store.Progress.Load()
	reads.require("progress", err)
	runMeta, err := t.store.RunMeta.Load()
	reads.require("run_meta", err)
	state.progress = progress
	state.runMeta = runMeta

	if runMeta != nil && runMeta.PlanningTier != "" {
		envelope.Episodic["planning_tier"] = runMeta.PlanningTier
	}
	if progress != nil && progress.TotalChapters > 0 {
		state.profile = domain.NewContextProfile(progress.TotalChapters)
	}
	if progress == nil || !progress.Layered {
		state.profile.Layered = false
	}

	outline, outlineErr := t.store.Outline.LoadOutline()
	reads.require("outline", outlineErr)
	state.outline = outline
	currentEntry := findOutlineEntry(outline, chapter)
	if currentEntry != nil {
		envelope.Working["current_chapter_outline"] = currentEntry
	}
	state.currentEntry = currentEntry

	chapterPlan, chapterPlanErr := t.store.Drafts.LoadChapterPlan(chapter)
	if chapterPlanErr == nil && chapterPlan != nil {
		envelope.Working["chapter_plan"] = chapterPlan
		if len(chapterPlan.Contract.RequiredBeats) > 0 ||
			len(chapterPlan.Contract.ForbiddenMoves) > 0 ||
			len(chapterPlan.Contract.ContinuityChecks) > 0 ||
			len(chapterPlan.Contract.EvaluationFocus) > 0 ||
			chapterPlan.Contract.EmotionTarget != "" ||
			len(chapterPlan.Contract.PayoffPoints) > 0 ||
			chapterPlan.Contract.HookGoal != "" {
			envelope.Working["chapter_contract"] = chapterPlan.Contract
		}
	} else {
		reads.require("chapter_plan", chapterPlanErr)
	}
	state.chapterPlan = chapterPlan

	// Whether this chapter is being rewritten: decides whether novel_context adds the "rewrite-specific" facts.
	isRewrite := progress != nil && slices.Contains(progress.PendingRewrites, chapter)

	// Surfaces the fact of whether a draft exists, so a re-dispatched writer can judge for itself whether to skip the
	// rewrite or overwrite. Only exists + word_count are exposed; the prose is not injected (the writer pulls it on
	// demand with read_chapter).
	if _, draftWords, draftErr := t.store.Drafts.LoadChapterContent(chapter); draftErr == nil && draftWords > 0 {
		envelope.Working["chapter_draft"] = map[string]any{
			"exists":     true,
			"word_count": draftWords,
		}
	} else if draftErr != nil {
		reads.require("chapter_draft", draftErr)
	}

	// On a rewrite, "why change it and where" goes to the writer: the reason comes from the rework queue and the
	// concrete criticism from this chapter's review (selectReviewLessons recalls only chapter-1..chapter-3, which
	// happens to miss this chapter itself, and the writer has no tool to read reviews). The prose is not injected here,
	// keeping intact the convention that prose is pulled on demand with read_chapter.
	if isRewrite {
		brief := map[string]any{"reason": progress.RewriteReason}
		if reviews, reviewErr := t.store.World.LoadReviewsAffectingChapter(chapter); reviewErr == nil {
			var sources []map[string]any
			for _, review := range reviews {
				item := map[string]any{
					"review_chapter": review.Chapter,
					"scope":          review.Scope,
					"summary":        review.Summary,
				}
				var issues []domain.ConsistencyIssue
				for _, issue := range review.Issues {
					// A new review dispatches precisely through its issue-to-chapter mapping; an old review without a
					// mapping keeps every issue, so historical rework reasons do not vanish after an upgrade.
					if len(issue.Chapters) == 0 || (issue.RequiresChange && slices.Contains(issue.Chapters, chapter)) {
						issues = append(issues, issue)
					}
				}
				if len(issues) > 0 {
					item["issues"] = issues
				}
				if review.Scope == "chapter" && len(review.ContractMisses) > 0 {
					item["contract_misses"] = review.ContractMisses
				}
				sources = append(sources, item)
			}
			if len(sources) > 0 {
				brief["reviews"] = sources
				// A single source keeps the old fields, so existing context consumers do not lose information on upgrade.
				if len(sources) == 1 {
					brief["review_summary"] = sources[0]["summary"]
					if issues, ok := sources[0]["issues"]; ok {
						brief["issues"] = issues
					}
					if misses, ok := sources[0]["contract_misses"]; ok {
						brief["contract_misses"] = misses
					}
				}
			}
		} else {
			reads.require("rewrite_review", reviewErr)
		}
		envelope.Working["rewrite_brief"] = brief
	}

	foreshadow, foreshadowErr := t.store.World.LoadActiveForeshadow()
	reads.require("foreshadow_ledger", foreshadowErr)
	state.foreshadow = foreshadow

	relationships, relErr := t.store.World.LoadRelationships()
	reads.require("relationship_state", relErr)
	if len(relationships) > 0 {
		envelope.Episodic["relationship_state"] = relationships
	}
	state.relationships = relationships

	allStateChanges, scErr := t.store.World.LoadStateChanges()
	reads.require("recent_state_changes", scErr)
	state.allStateChanges = allStateChanges
	if len(allStateChanges) > 0 {
		start := max(chapter-2, 1)
		var recent []domain.StateChange
		for _, c := range allStateChanges {
			if c.Chapter >= start && c.Chapter < chapter {
				recent = append(recent, c)
			}
		}
		if len(recent) > 0 {
			envelope.Episodic["recent_state_changes"] = recent
		}
	}

	styleRules, styleErr := t.store.World.LoadStyleRules()
	reads.require("style_rules", styleErr)
	state.styleRules = styleRules
	state.storyThreads = t.selectStoryThreads(state)
	if len(state.storyThreads) > 0 && len(state.storyThreads) < storyThreadRecallMinSelected {
		state.storyThreads = nil
	}

	return state
}

func (t *ContextTool) buildChapterContext(result map[string]any, state contextBuildState, reads *contextReads) {
	envelope := newChapterContextEnvelope()
	result["memory_policy"] = domain.NewChapterMemoryPolicy(state.progress, state.profile, state.currentEntry != nil)

	if state.profile.Layered {
		t.loadLayeredCharacters(envelope.Episodic, state.chapter, reads)
	} else {
		t.loadFilteredCharacters(envelope.Episodic, state.chapter, reads)
	}

	t.buildChapterEpisodicMemory(&envelope, state, reads)
	t.buildChapterWorkingMemory(&envelope, state, reads)
	t.buildChapterReferencePack(&envelope, state, reads)
	t.buildChapterSelectedMemory(&envelope, state, reads)
	t.buildStyleStats(&envelope, state, reads)
	envelope.apply(result)
}

// buildStyleStats computes book-level style statistics over every completed chapter and injects them at
// episodic_memory.style_stats.
// An in-arc review window is naturally blind to "a sentence tic dozens of times per chapter, identical chapter-ending
// shapes, cross-chapter repetition", and only book-level statistics expose them — statistics belong to code
// (deterministic) and the verdict to the LLM (the editor scores the aesthetic dimension from the numbers and the
// writer self-avoids from them). stylestat returns nil when there are too few chapters, and nothing is injected.
func (t *ContextTool) buildStyleStats(envelope *chapterContextEnvelope, state contextBuildState, reads *contextReads) {
	if state.progress == nil || len(state.progress.CompletedChapters) == 0 {
		return
	}

	var titles []string
	if outline, err := t.store.Outline.LoadOutline(); err == nil {
		for _, entry := range outline {
			titles = append(titles, entry.Title)
		}
	} else {
		reads.warn("style_stats.outline", err)
	}

	stats, err := t.styleStats.Snapshot(
		state.progress.CompletedChapters,
		titles,
		t.styleStopwords(reads),
	)
	if err != nil {
		reads.warn("style_stats", err)
		return
	}
	if stats == nil {
		return
	}
	envelope.Episodic["style_stats"] = stats
}

// styleStopwords collects character names and aliases to filter phrase mining — appearing names are naturally high-frequency and are not a style problem.
func (t *ContextTool) styleStopwords(reads *contextReads) []string {
	var words []string
	if chars, err := t.store.Characters.Load(); err == nil {
		for _, c := range chars {
			words = append(words, c.Name)
			words = append(words, c.Aliases...)
		}
	} else {
		reads.warn("style_stats.characters", err)
	}
	if cast, err := t.store.Cast.RecentActive(50); err == nil {
		for _, e := range cast {
			words = append(words, e.Name)
			words = append(words, e.Aliases...)
		}
	} else {
		reads.warn("style_stats.cast", err)
	}
	return words
}

func (t *ContextTool) buildChapterWorkingMemory(envelope *chapterContextEnvelope, state contextBuildState, reads *contextReads) {
	t.buildOutlineWindow(envelope.Working, state, reads)
	if next := findOutlineEntry(state.outline, state.chapter+1); next != nil {
		envelope.Working["next_chapter_outline"] = next
	}

	if state.profile.Layered {
		t.loadLayeredSummaries(envelope.Working, state.chapter, state.profile.SummaryWindow, reads)
		// Closing discipline: injected when this chapter belongs to a declared closing volume, stopping the writer from
		// opening new hooks near the end of the closing stretch (a closing volume completes automatically once written,
		// so a foreshadow planted then would never get a chance to be recovered).
		if volumes, err := t.store.Outline.LoadLayeredOutline(); err == nil {
			if fv := domain.FinaleVolume(volumes); fv > 0 {
				if b, boundaryErr := t.store.Outline.CheckArcBoundary(state.chapter); boundaryErr == nil && b != nil && b.Volume == fv {
					envelope.Working["finale"] = "Tập này là tập kết thúc toàn sách: không mở tuyến dài mới hay gieo phục bút mới, ưu tiên thu hồi phục bút sẵn có, thu gọn các tuyến quan hệ, đẩy câu chuyện tới kết cục theo đại cương."
				} else {
					reads.require("arc_boundary", boundaryErr)
				}
			}
		} else {
			reads.require("layered_outline", err)
		}
	} else {
		if summaries, err := t.store.Summaries.LoadRecentSummaries(state.chapter, state.profile.SummaryWindow); err == nil && len(summaries) > 0 {
			envelope.Working["recent_summaries"] = summaries
		} else {
			reads.require("recent_summaries", err)
		}
	}

	if timeline, err := t.store.World.LoadRecentTimeline(state.chapter, state.profile.TimelineWindow); err == nil && len(timeline) > 0 {
		envelope.Working["timeline"] = timeline
	} else {
		reads.require("timeline", err)
	}

	if state.progress != nil {
		checkpoint := map[string]any{
			"in_progress_chapter": state.progress.InProgressChapter,
		}
		if len(state.progress.StrandHistory) > 0 {
			checkpoint["strand_history"] = state.progress.StrandHistory
		}
		if len(state.progress.HookHistory) > 0 {
			checkpoint["hook_history"] = state.progress.HookHistory
			// Counts are given alongside: handed only a sequence, the model counts for itself and counts wrong — an
			// editor was measured reporting "about 18/22 chapters are mystery" three times running when the real value
			// was 16. Any fact that can be computed is computed by code and then handed over.
			checkpoint["hook_type_counts"] = countByValue(state.progress.HookHistory)
		}
		envelope.Working["checkpoint"] = checkpoint
	}

	if state.chapter > 1 {
		if prevText, err := t.store.Drafts.LoadChapterText(state.chapter - 1); err == nil && prevText != "" {
			runes := []rune(prevText)
			if len(runes) > 800 {
				runes = runes[len(runes)-800:]
			}
			envelope.Working["previous_tail"] = string(runes)
		} else {
			reads.require("previous_chapter", err)
		}
	}
}

// buildOutlineWindow keeps the outline directly relevant to the current task for the Writer/Editor, instead of
// injecting a full flat outline that grows with the book. Layered mode uses the current arc; non-layered mode uses the
// most recent review cycle.
func (t *ContextTool) buildOutlineWindow(working map[string]any, state contextBuildState, reads *contextReads) {
	outline := state.outline
	if len(outline) == 0 {
		return
	}

	start := max(1, state.chapter-domain.ReviewInterval+1)
	end := min(state.chapter, len(outline))
	if state.profile.Layered {
		boundary, err := t.store.Outline.CheckArcBoundary(state.chapter)
		if err != nil {
			reads.require("outline_window.arc_boundary", err)
			return
		}
		if boundary == nil {
			return
		}
		start = boundary.StartChapter
		end = min(boundary.EndChapter, len(outline))
	}
	if start <= end {
		working["outline_window"] = outline[start-1 : end]
	}
}

func findOutlineEntry(outline []domain.OutlineEntry, chapter int) *domain.OutlineEntry {
	for i := range outline {
		if outline[i].Chapter == chapter {
			return &outline[i]
		}
	}
	return nil
}

func (t *ContextTool) buildChapterSelectedMemory(envelope *chapterContextEnvelope, state contextBuildState, reads *contextReads) {
	if len(state.storyThreads) > 0 {
		envelope.Selected["story_threads"] = state.storyThreads
	}
	if lessons := t.selectReviewLessons(state.chapter, reads); len(lessons) > 0 {
		envelope.Selected["review_lessons"] = lessons
	}
}

func (t *ContextTool) buildChapterEpisodicMemory(envelope *chapterContextEnvelope, state contextBuildState, reads *contextReads) {
	if len(state.foreshadow) > 0 && len(state.storyThreads) == 0 {
		envelope.Episodic["foreshadow_ledger"] = state.foreshadow
	}

	// Supporting-cast roster: recalls the most recently active supporting characters so the Writer keeps voice and
	// positioning consistent when reintroducing old ones. Not every entry is recalled (a long book would balloon), only
	// the most recently active N, ordered by LastSeenChapter descending
	if recentCast, err := t.store.Cast.RecentActive(15); err == nil && len(recentCast) > 0 {
		simplified := make([]map[string]any, 0, len(recentCast))
		for _, e := range recentCast {
			item := map[string]any{
				"name":             e.Name,
				"first_seen":       e.FirstSeenChapter,
				"last_seen":        e.LastSeenChapter,
				"appearance_count": e.AppearanceCount,
			}
			if e.BriefRole != "" {
				item["brief_role"] = e.BriefRole
			}
			if len(e.Aliases) > 0 {
				item["aliases"] = e.Aliases
			}
			simplified = append(simplified, item)
		}
		envelope.Episodic["recent_cast"] = simplified
	} else if err != nil {
		reads.warn("recent_cast", err)
	}

	if state.progress != nil && state.progress.TotalChapters > 30 && state.currentEntry != nil {
		if related := t.buildRelatedChapters(
			state.chapter,
			state.currentEntry,
			state.foreshadow,
			state.relationships,
			state.allStateChanges,
			reads,
		); len(related) > 0 {
			envelope.Episodic["related_chapters"] = related
		}
	}

	if state.profile.Layered && state.progress != nil {
		pos := map[string]any{
			"volume": state.progress.CurrentVolume,
			"arc":    state.progress.CurrentArc,
		}
		if volumes, err := t.store.Outline.LoadLayeredOutline(); err == nil {
			globalCh := 1
			for _, v := range volumes {
				if v.Index == state.progress.CurrentVolume {
					pos["volume_title"] = v.Title
					pos["volume_theme"] = v.Theme
				}
				for _, arc := range v.Arcs {
					if v.Index == state.progress.CurrentVolume && arc.Index == state.progress.CurrentArc {
						pos["arc_title"] = arc.Title
						pos["arc_goal"] = arc.Goal
						if n := len(arc.Chapters); n > 0 {
							pos["arc_total_chapters"] = n
							pos["arc_chapter_index"] = state.chapter - globalCh + 1
						}
					}
					globalCh += len(arc.Chapters)
				}
			}
		} else {
			reads.require("layered_outline", err)
		}
		envelope.Episodic["position"] = pos
	}
}

func (t *ContextTool) buildChapterReferencePack(envelope *chapterContextEnvelope, state contextBuildState, reads *contextReads) {
	authorStyle, err := t.store.World.LoadAuthorRevisionStyle()
	reads.warn("author_revision_style", err)
	if authorStyle != nil && (len(authorStyle.Prose) > 0 || len(authorStyle.Dialogue) > 0 || len(authorStyle.Taboos) > 0) {
		envelope.References["author_revision_style"] = authorStyle
	}

	if state.styleRules != nil {
		envelope.References["style_rules"] = state.styleRules
	} else {
		var maxCompleted int
		if state.progress != nil {
			maxCompleted = maxCompletedChapter(state.progress.CompletedChapters)
		}
		anchors, err := t.store.Drafts.ExtractStyleAnchors(3, maxCompleted)
		reads.warn("style_anchors", err)
		if len(anchors) > 0 {
			envelope.References["style_anchors"] = anchors
		}

		if state.currentEntry != nil {
			var voiceSamples []map[string]any
			chars, err := t.store.Characters.Load()
			reads.warn("voice_samples.characters", err)
			for _, c := range chars {
				if c.Tier == "secondary" || c.Tier == "decorative" {
					continue
				}
				samples, err := t.store.Drafts.ExtractDialogue(c.Name, c.Aliases, 3, maxCompleted)
				reads.warn("voice_samples."+c.Name, err)
				if len(samples) > 0 {
					voiceSamples = append(voiceSamples, map[string]any{
						"character": c.Name,
						"samples":   samples,
					})
				}
				if len(voiceSamples) >= 5 {
					break
				}
			}
			if len(voiceSamples) > 0 {
				envelope.References["voice_samples"] = voiceSamples
			}
		}
	}

	envelope.References["references"] = t.writerReferences(state.chapter)
}

func (t *ContextTool) buildArchitectContext(result map[string]any, reads *contextReads) {
	envelope := newArchitectContextEnvelope()
	result["memory_policy"] = domain.NewArchitectMemoryPolicy()
	t.buildArchitectPlanning(&envelope, reads)
	t.buildArchitectFoundation(&envelope, reads)
	t.buildArchitectReferences(&envelope, reads)
	envelope.apply(result)
}

func (t *ContextTool) buildArchitectPlanning(envelope *architectContextEnvelope, reads *contextReads) {
	runMeta, err := t.store.RunMeta.Load()
	reads.require("run_meta", err)
	if runMeta != nil && runMeta.PlanningTier != "" {
		envelope.Planning["planning_tier"] = runMeta.PlanningTier
	}
	progress, progressErr := t.store.Progress.Load()
	reads.require("progress_for_planning", progressErr)

	var layered []domain.VolumeOutline
	if l, err := t.store.Outline.LoadLayeredOutline(); err == nil && len(l) > 0 {
		layered = l
		latestCompleted := 0
		if progress != nil {
			latestCompleted = progress.LatestCompleted()
		}
		if latestCompleted > 0 {
			envelope.Planning["layered_outline"] = projectLayeredOutlineForPlanning(layered, latestCompleted)
		} else {
			envelope.Planning["layered_outline"] = layered
		}
		var skeletonArcs []map[string]any
		for _, v := range layered {
			for _, a := range v.Arcs {
				if !a.IsExpanded() {
					skeletonArcs = append(skeletonArcs, map[string]any{
						"volume":             v.Index,
						"arc":                a.Index,
						"title":              a.Title,
						"goal":               a.Goal,
						"estimated_chapters": a.EstimatedChapters,
					})
				}
			}
		}
		if len(skeletonArcs) > 0 {
			envelope.Planning["skeleton_arcs"] = skeletonArcs
		}
	} else {
		reads.require("layered_outline", err)
	}
	if len(layered) == 0 {
		if outline, err := t.store.Outline.LoadOutline(); err == nil && len(outline) > 0 {
			envelope.Planning["outline"] = outline
		} else {
			reads.require("outline", err)
		}
	}

	var compass *domain.StoryCompass
	if c, err := t.store.Outline.LoadCompass(); err == nil && c != nil {
		compass = c
		envelope.Planning["compass"] = compass
	} else {
		reads.require("compass", err)
	}
	if volSummaries, err := t.store.Summaries.LoadAllVolumeSummaries(); err == nil && len(volSummaries) > 0 {
		envelope.Planning["volume_summaries"] = volSummaries
	} else {
		reads.require("volume_summaries", err)
	}
	// Volume summaries carry the completed volumes and the current volume's arc summaries carry the latest actual plot.
	// When expanding an arc both go to the Architect together with the skeleton goal, letting the model decide for
	// itself whether to keep or revise the unwritten plan.
	if progressErr == nil && progress != nil && progress.CurrentVolume > 0 {
		if arcSummaries, err := t.store.Summaries.LoadArcSummaries(progress.CurrentVolume); err == nil && len(arcSummaries) > 0 {
			envelope.Planning["arc_summaries"] = arcSummaries
		} else {
			reads.require("arc_summaries", err)
		}
	} else {
		reads.require("progress_for_arc_summaries", progressErr)
	}

	// completion_signals presents the key facts of "should the book end" in one place, so the architect sees the
	// comparison at a glance when ruling complete_book / append_volume. Scattered across progress / compass /
	// foreshadow / layered_outline they are easy for the LLM to miss when computed mentally.
	envelope.Planning["completion_signals"] = t.completionSignals(layered, compass, reads)
}

func projectLayeredOutlineForPlanning(volumes []domain.VolumeOutline, latestCompleted int) []planningVolumeOutline {
	projected := make([]planningVolumeOutline, 0, len(volumes))
	chapter := 1
	for _, volume := range volumes {
		pv := planningVolumeOutline{
			Index: volume.Index, Title: volume.Title, Theme: volume.Theme, Final: volume.Final,
			Arcs: make([]planningArcOutline, 0, len(volume.Arcs)),
		}
		for _, arc := range volume.Arcs {
			pa := planningArcOutline{
				Index: arc.Index, Title: arc.Title, Goal: arc.Goal,
				EstimatedChapters: arc.EstimatedChapters,
			}
			if len(arc.Chapters) == 0 {
				pa.Status = "skeleton"
				pv.Arcs = append(pv.Arcs, pa)
				continue
			}
			pa.StartChapter = chapter
			pa.EndChapter = chapter + len(arc.Chapters) - 1
			pa.ChapterCount = len(arc.Chapters)
			if pa.EndChapter <= latestCompleted {
				pa.Status = "completed"
			} else {
				pa.Status = "expanded"
				pa.Chapters = arc.Chapters
			}
			chapter = pa.EndChapter + 1
			pv.Arcs = append(pv.Arcs, pa)
		}
		projected = append(projected, pv)
	}
	return projected
}

func (t *ContextTool) completionSignals(layered []domain.VolumeOutline, compass *domain.StoryCompass, reads *contextReads) map[string]any {
	signals := map[string]any{}
	if progress, err := t.store.Progress.Load(); progress != nil {
		signals["completed_chapters"] = len(progress.CompletedChapters)
		signals["total_word_count"] = progress.TotalWordCount
		signals["phase"] = string(progress.Phase)
	} else {
		reads.require("completion_signals.progress", err)
	}
	if len(layered) > 0 {
		signals["planned_chapters"] = len(domain.FlattenOutline(layered))
		signals["volumes_total"] = len(layered)
		if fv := domain.FinaleVolume(layered); fv > 0 {
			signals["final_volume"] = fv
		}
	}
	if compass != nil {
		if compass.EstimatedScale != "" {
			signals["compass_estimated_scale"] = compass.EstimatedScale
		}
		signals["open_threads_count"] = len(compass.OpenThreads)
	}
	if active, err := t.store.World.LoadActiveForeshadow(); err == nil {
		signals["active_foreshadow_count"] = len(active)
	} else {
		reads.require("completion_signals.foreshadow", err)
	}
	return signals
}

func (t *ContextTool) buildArchitectFoundation(envelope *architectContextEnvelope, reads *contextReads) {
	if book, err := t.store.Book.Load(); err == nil && book != nil {
		envelope.Foundation["book"] = book
	} else {
		reads.require("book", err)
	}
	if premise, err := t.store.Outline.LoadPremise(); err == nil && premise != "" {
		envelope.Foundation["premise"] = premise
		if sections := parsePremiseSections(premise); len(sections) > 0 {
			envelope.Foundation["premise_sections"] = sections
		}
		tier := domain.PlanningTier("")
		if meta, err := t.store.RunMeta.Load(); err == nil && meta != nil {
			tier = meta.PlanningTier
		} else {
			reads.require("run_meta", err)
		}
		envelope.Foundation["premise_structure"] = premiseStructure(premise, tier)
	} else {
		reads.require("premise", err)
	}

	if chars, err := t.store.Characters.Load(); err == nil && chars != nil {
		envelope.Foundation["characters"] = chars
	} else {
		reads.require("characters", err)
	}

	if snapshots, err := t.store.Characters.LoadLatestSnapshots(); err == nil && len(snapshots) > 0 {
		envelope.Foundation["character_snapshots"] = snapshots
	} else {
		reads.require("character_snapshots", err)
	}
	if rules, err := t.store.World.LoadWorldRules(); err == nil && len(rules) > 0 {
		envelope.Foundation["world_rules"] = rules
	} else {
		reads.require("world_rules", err)
	}
	if foreshadow, err := t.store.World.LoadActiveForeshadow(); err == nil && len(foreshadow) > 0 {
		envelope.Foundation["foreshadow_ledger"] = foreshadow
	} else {
		reads.require("foreshadow_ledger", err)
	}
	if status, err := t.foundationStatus(); err == nil {
		envelope.Foundation["foundation_status"] = status
	} else {
		reads.require("foundation_status", err)
	}
	// Writer feedback pool: outline deviations and suggestions persisted by commit_chapter, which must be consulted when
	// planning the next arc or volume; cleared automatically once expand_arc / append_volume / update_compass succeed
	// (consumed).
	if fbs, err := t.store.Outline.LoadPendingOutlineFeedback(); err == nil && len(fbs) > 0 {
		envelope.Foundation["writer_feedback"] = fbs
	} else {
		reads.require("writer_feedback", err)
	}
}

func (t *ContextTool) buildArchitectReferences(envelope *architectContextEnvelope, reads *contextReads) {
	if styleRules, err := t.store.World.LoadStyleRules(); err == nil && styleRules != nil {
		envelope.References["style_rules"] = styleRules
	} else {
		reads.warn("style_rules", err)
	}

	envelope.References["references"] = t.architectReferences()
}

// countByValue counts value frequencies so "countable facts" in the context become numbers directly.
func countByValue(values []string) map[string]int {
	out := make(map[string]int, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		out[v]++
	}
	return out
}
