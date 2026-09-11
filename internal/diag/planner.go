package diag

import "fmt"

// PlanActions generates executable actions from high-confidence Findings.
// Only Findings with Confidence==high && AutoLevel==safe produce an Action.
func PlanActions(findings []Finding) []Action {
	var actions []Action
	seen := make(map[string]struct{})

	for _, f := range findings {
		if f.Confidence != ConfHigh || f.AutoLevel != AutoSafe {
			continue
		}
		if _, ok := seen[f.Rule]; ok {
			continue
		}
		seen[f.Rule] = struct{}{}

		actions = append(actions, planRule(f)...)
	}
	return actions
}

func planRule(f Finding) []Action {
	key := findingFingerprint(f)

	switch f.Rule {
	case "PhaseFlowMismatch":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEmitNotice, Severity: f.Severity, Summary: f.Title, Message: f.Title, Fingerprint: key},
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "Sửa bất thường máy trạng thái", Message: "Máy trạng thái bất thường: " + f.Evidence + ". Hãy kiểm tra và sửa trạng thái phase/flow trong progress trước rồi mới chạy tiếp.", Fingerprint: key},
		}
	case "OutlineExhausted":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "Xử lý cạn đại cương", Message: "Số chương đã hoàn thành đạt giới hạn đã quy hoạch. Hãy ưu tiên gọi Architect để mở rộng cung kế tiếp hoặc thêm tập mới, rồi mới viết tiếp.", Fingerprint: key},
		}
	case "OrphanedSteer":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "Xử lý can thiệp người dùng chưa tiêu thụ", Message: "Còn chỉ thị can thiệp của người dùng chưa được xử lý, hãy xử lý pending steer trước rồi mới tiếp tục nhiệm vụ hiện tại.", Fingerprint: key},
		}
	default:
		return nil
	}
}

func findingFingerprint(f Finding) string {
	return fmt.Sprintf("%s|%s|%s|%s", f.Rule, f.Target, f.Title, f.Evidence)
}
