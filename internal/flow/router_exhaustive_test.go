package flow

// Exhaustive state-space test for Route.
//
// expectedInstruction is an independent mirror of the decision table (an executable spec matching the
// 11-branch priority of architecture.md's second iron law), deliberately reusing none of the
// implementation's code: if a refactor shifts behaviour, this goes red immediately; changing behaviour
// requires changing the spec too and leaving a diff. The single-branch cases in router_test.go provide
// readable intent documentation while this file covers priority and conservation properties over the
// whole combination space.

import (
	"reflect"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// expectKind is the spec-level verdict: who to route to and what class of work to do.
type expectKind int

const (
	expectNil expectKind = iota
	expectRewrite
	expectArcReview
	expectArcSummary
	expectVolumeSummary
	expectExpandArc
	expectNewVolume
	expectNextChapter
	expectFoundationFill
	expectGlobalReview
	expectOutlineFeedback
)

// expectedInstruction computes the verdict a State should get under the architecture spec.
// Priority (the first hit from the top):
//  1. Progress missing / Phase terminal → LLM verdict (nil)
//  2. Planning stage (not writing): a foundation item is missing and the planner is identifiable
//     (save_foundation has already landed scale)
//     → re-dispatch the same planner for the gap; otherwise → LLM verdict (nil, including the first
//     planner type choice)
//  3. Rewrite/polish queue non-empty → the writer takes the queue head (absolute priority, overriding
//     every arc-end affair)
//  4. Flow=Reviewing / Steering → LLM verdict (nil)
//  5. A missing aggregate artifact → Editor fills it in
//  6. An external revision affects later planning → Architect consumes it
//  7. Layered arc end → review → arc summary → (volume end) volume summary → expand the next arc →
//     append a new volume
//  8. Everything else → the writer continues with the next chapter
func expectedInstruction(s State) expectKind {
	p := s.Progress
	if p == nil || p.Phase == domain.PhaseComplete {
		return expectNil
	}
	if p.Phase != domain.PhaseWriting {
		if len(s.FoundationMissing) > 0 && s.PlanningTier != "" {
			return expectFoundationFill
		}
		return expectNil
	}
	if len(p.PendingRewrites) > 0 {
		return expectRewrite
	}
	if p.Flow == domain.FlowReviewing || p.Flow == domain.FlowSteering {
		return expectNil
	}
	if s.AggregateRefresh != nil {
		return expectArcSummary
	}
	if s.ImmediateFeedbackCount > 0 {
		return expectOutlineFeedback
	}
	if p.Layered && s.ArcBoundary != nil && s.ArcBoundary.IsArcEnd {
		b := s.ArcBoundary
		switch {
		case !s.HasArcReview:
			return expectArcReview
		case !s.HasArcSummary:
			return expectArcSummary
		case b.IsVolumeEnd && !s.HasVolumeSummary:
			return expectVolumeSummary
		case b.NeedsExpansion && b.NextArc > 0:
			return expectExpandArc
		case b.NeedsNewVolume:
			return expectNewVolume
		}
	}
	// Non-layered: one global review every ReviewInterval chapters (if absent, review first then continue).
	if !p.Layered && s.LastCompleted > 0 {
		if due, _ := domain.ShouldReview(len(p.CompletedChapters)); due && !s.HasGlobalReview {
			return expectGlobalReview
		}
	}
	return expectNextChapter
}

// classify maps the Instruction the implementation returns onto a spec category; an unrecognised combination fails outright.
func classify(t *testing.T, inst *Instruction) expectKind {
	t.Helper()
	if inst == nil {
		return expectNil
	}
	switch inst.Agent {
	case "writer":
		switch {
		case contains(inst.Task, "viết lại") || contains(inst.Task, "gọt giũa"):
			return expectRewrite
		case contains(inst.Task, "Viết chương"):
			return expectNextChapter
		}
	case "editor":
		switch {
		case contains(inst.Task, "Thẩm duyệt cấp cung"):
			return expectArcReview
		case contains(inst.Task, "Thẩm duyệt toàn cục"):
			return expectGlobalReview
		case contains(inst.Task, "save_arc_summary"):
			return expectArcSummary
		case contains(inst.Task, "save_volume_summary"):
			return expectVolumeSummary
		}
	case "architect_long":
		switch {
		case contains(inst.Task, "Bổ sung các mục thiếu của thiết lập nền tảng"):
			return expectFoundationFill
		case contains(inst.Task, "writer_feedback"):
			return expectOutlineFeedback
		case contains(inst.Task, "expand_arc"):
			return expectExpandArc
		case contains(inst.Task, "append_volume"):
			return expectNewVolume
		}
	case "architect_short":
		if contains(inst.Task, "Bổ sung các mục thiếu của thiết lập nền tảng") {
			return expectFoundationFill
		}
		if contains(inst.Task, "writer_feedback") {
			return expectOutlineFeedback
		}
	}
	t.Fatalf("无法归类的指令：agent=%q task=%q", inst.Agent, inst.Task)
	return expectNil
}

// boundaryCase is one enumeration point of the arc-boundary dimension: the boundary shape plus three summary facts.
type boundaryCase struct {
	name             string
	boundary         *storepkg.ArcBoundary
	hasArcReview     bool
	hasArcSummary    bool
	hasVolumeSummary bool
}

func enumerateBoundaryCases() []boundaryCase {
	cases := []boundaryCase{
		{name: "no-boundary"},
		{name: "mid-arc", boundary: &storepkg.ArcBoundary{Volume: 1, Arc: 1}},
	}
	type volCase struct {
		name       string
		volumeEnd  bool
		volSummary bool
	}
	type followCase struct {
		name      string
		expansion bool
		nextArc   int
		newVolume bool
	}
	volCases := []volCase{
		{name: "vol-mid", volumeEnd: false},
		{name: "vol-end-nosum", volumeEnd: true, volSummary: false},
		{name: "vol-end-sum", volumeEnd: true, volSummary: true},
	}
	followCases := []followCase{
		{name: "settled"},
		{name: "expand", expansion: true, nextArc: 4},
		{name: "expand-no-nextarc", expansion: true, nextArc: 0}, // 展开位缺失 → 不可展开
		{name: "new-volume", newVolume: true},
	}
	for _, review := range []bool{false, true} {
		for _, summary := range []bool{false, true} {
			for _, vc := range volCases {
				for _, fc := range followCases {
					cases = append(cases, boundaryCase{
						name: fmtBool("rev", review) + fmtBool("+sum", summary) + "+" + vc.name + "+" + fc.name,
						boundary: &storepkg.ArcBoundary{
							IsArcEnd:       true,
							IsVolumeEnd:    vc.volumeEnd,
							Volume:         2,
							Arc:            3,
							NextVolume:     2,
							NextArc:        fc.nextArc,
							NeedsExpansion: fc.expansion,
							NeedsNewVolume: fc.newVolume,
						},
						hasArcReview:     review,
						hasArcSummary:    summary,
						hasVolumeSummary: vc.volSummary,
					})
				}
			}
		}
	}
	return cases
}

func fmtBool(label string, v bool) string {
	if v {
		return label
	}
	return label + "!"
}

func TestRoute_ExhaustiveAgainstSpec(t *testing.T) {
	phases := []domain.Phase{domain.PhaseInit, domain.PhasePremise, domain.PhaseOutline, domain.PhaseWriting, domain.PhaseComplete}
	flows := []domain.FlowState{domain.FlowWriting, domain.FlowReviewing, domain.FlowRewriting, domain.FlowPolishing, domain.FlowSteering}
	queues := [][]int{nil, {7, 9}}
	// {1..5} hits the global-review trigger point at ReviewInterval(=5)
	completedSets := [][]int{nil, {1, 2, 3}, {1, 2, 3, 4, 5}}
	missingSets := [][]string{nil, {"characters", "world_rules"}}
	tiers := []domain.PlanningTier{"", domain.PlanningTierShort, domain.PlanningTierLong}
	globalReviews := []bool{false, true}
	feedbackCounts := []int{0, 1}
	aggregates := []*AggregateRefresh{nil, {Kind: AggregateArcSummary, Volume: 1, Arc: 1, EndChapter: 5}}

	total := 0
	for _, phase := range phases {
		for _, fl := range flows {
			for _, queue := range queues {
				for _, layered := range []bool{false, true} {
					for _, completed := range completedSets {
						for _, missing := range missingSets {
							for _, tier := range tiers {
								for _, hasGlobal := range globalReviews {
									for _, feedbackCount := range feedbackCounts {
										for _, aggregate := range aggregates {
											for _, bc := range enumerateBoundaryCases() {
												total++
												p := &domain.Progress{
													Phase:             phase,
													Flow:              fl,
													Layered:           layered,
													CompletedChapters: append([]int(nil), completed...),
													PendingRewrites:   append([]int(nil), queue...),
												}
												last := 0
												if n := len(completed); n > 0 {
													last = completed[n-1]
												}
												s := State{
													Progress:               p,
													LastCompleted:          last,
													ArcBoundary:            bc.boundary,
													HasArcReview:           bc.hasArcReview,
													HasArcSummary:          bc.hasArcSummary,
													HasVolumeSummary:       bc.hasVolumeSummary,
													FoundationMissing:      append([]string(nil), missing...),
													PlanningTier:           tier,
													HasGlobalReview:        hasGlobal,
													ImmediateFeedbackCount: feedbackCount,
													AggregateRefresh:       aggregate,
												}

												before := snapshotState(s)
												inst := Route(s)
												want := expectedInstruction(s)
												got := classify(t, inst)
												if got != want {
													t.Fatalf("phase=%s flow=%s queue=%v layered=%v completed=%v missing=%v tier=%q global=%v boundary=%s:\n规格期望 %d，实现返回 %d（inst=%+v）",
														phase, fl, queue, layered, completed, missing, tier, hasGlobal, bc.name, want, got, inst)
												}
												assertConservation(t, s, inst)
												if !reflect.DeepEqual(before, snapshotState(s)) {
													t.Fatalf("Route 必须是纯函数，不得改写输入 State（boundary=%s）", bc.name)
												}
												if again := Route(s); !reflect.DeepEqual(inst, again) {
													t.Fatalf("Route 必须确定：两次调用结果不同（boundary=%s）", bc.name)
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if total < 5000 {
		t.Fatalf("枚举空间意外缩水（%d 组合），检查维度枚举", total)
	}
}

// assertConservation covers conservation properties independent of the specific branch.
func assertConservation(t *testing.T, s State, inst *Instruction) {
	t.Helper()
	if inst == nil {
		return
	}
	p := s.Progress
	if p == nil || p.Phase == domain.PhaseComplete {
		t.Fatalf("终态或无进度时不得产生指令：%+v", inst)
	}
	if p.Phase != domain.PhaseWriting {
		// The only legitimate instruction during planning: a catch-up dispatch whose planner matches the persisted tier
		wantPlanner := "architect_long"
		if s.PlanningTier == domain.PlanningTierShort {
			wantPlanner = "architect_short"
		}
		if inst.Agent != wantPlanner || !contains(inst.Task, "Bổ sung các mục thiếu của thiết lập nền tảng") || inst.Chapter != 0 {
			t.Fatalf("规划期指令必须是补齐派单且规划师匹配 tier=%q：%+v", s.PlanningTier, inst)
		}
		return
	}
	switch inst.Agent {
	case "writer":
		if inst.Chapter <= 0 {
			t.Fatalf("writer 指令必须带章节号：%+v", inst)
		}
		if len(p.PendingRewrites) > 0 {
			if inst.Chapter != p.PendingRewrites[0] {
				t.Fatalf("重写队列非空时必须派队列头 %d，got %d", p.PendingRewrites[0], inst.Chapter)
			}
			wantVerb := "viết lại"
			if p.Flow == domain.FlowPolishing {
				wantVerb = "gọt giũa"
			}
			if !contains(inst.Task, wantVerb) {
				t.Fatalf("队列任务动词应为 %q：%q", wantVerb, inst.Task)
			}
		} else if inst.Chapter != p.NextChapter() {
			t.Fatalf("续写指令章节号应为 NextChapter=%d，got %d", p.NextChapter(), inst.Chapter)
		}
	case "editor", "architect_long", "architect_short":
		if inst.Chapter != 0 {
			t.Fatalf("%s 指令不应带章节号：%+v", inst.Agent, inst)
		}
	default:
		t.Fatalf("未知路由目标 %q", inst.Agent)
	}
	if inst.Task == "" || inst.Reason == "" {
		t.Fatalf("指令的 Task 与 Reason 都不得为空：%+v", inst)
	}
}

// snapshotState deep-copies State for pure-function assertions.
func snapshotState(s State) State {
	cp := s
	if s.Progress != nil {
		p := *s.Progress
		p.CompletedChapters = append([]int(nil), s.Progress.CompletedChapters...)
		p.PendingRewrites = append([]int(nil), s.Progress.PendingRewrites...)
		cp.Progress = &p
	}
	if s.ArcBoundary != nil {
		b := *s.ArcBoundary
		cp.ArcBoundary = &b
	}
	if s.AggregateRefresh != nil {
		refresh := *s.AggregateRefresh
		cp.AggregateRefresh = &refresh
	}
	cp.FoundationMissing = append([]string(nil), s.FoundationMissing...)
	return cp
}
