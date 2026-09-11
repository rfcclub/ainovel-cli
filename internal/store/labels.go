package store

// mdLabels holds the fixed labels in derived Markdown views.
//
// These .md files are read back into the model's context by novel_context, so the language of the labels is not merely a
// display concern: Vietnamese prose with every chapter headed "第 N 章" / "核心事件" constantly suggests to the
// model that the current context is Chinese, and was measured to make the prose drag in Han characters
// (quan sát草木 / Bạo虐 / the ideographic comma). Labels follow the work's language so they do not tug against
// the model's output language.
type mdLabels struct {
	bookTitleFmt string
	synopsis     string
	charProfiles string
	charArc      string
	traits       string
	listSep      string
	openParen    string
	closeParen   string
	colon        string

	outline        string
	layeredOutline string
	volumeFmt      string
	arcFmt         string
	chapterFmt     string
	theme          string
	goal           string
	pendingArcFmt  string
	coreEvent      string
	hook           string
	scenes         string

	timeline         string
	foreshadow       string
	resolvedAtFmt    string
	plantedAtFmt     string
	relationships    string
	atChapterFmt     string
	worldRules       string
	rule             string
	boundary         string
}

// labelsVI uses the project's established renderings: tập / cung / chương / đề cương / điểm móc / phục bút.
var labelsVI = mdLabels{
	bookTitleFmt: "%s", synopsis: "Giới thiệu", charProfiles: "Hồ sơ nhân vật", charArc: "Cung nhân vật",
	traits: "Đặc điểm", listSep: ", ", openParen: " (", closeParen: ")", colon: ": ",

	outline: "Đề cương", layeredOutline: "Đề cương phân tầng",
	volumeFmt: "Tập %d", arcFmt: "Cung %d", chapterFmt: "Chương %d",
	theme: "Chủ đề", goal: "Mục tiêu", pendingArcFmt: "*(chưa khai triển, ước %d chương)*",
	coreEvent: "Sự kiện chính", hook: "Điểm móc", scenes: "Cảnh",

	timeline: "Dòng thời gian", foreshadow: "Sổ phục bút",
	resolvedAtFmt: "đã thu ở chương %d", plantedAtFmt: "gieo ở chương %d, trạng thái: %s",
	relationships: "Quan hệ nhân vật", atChapterFmt: " (chương %d)",
	worldRules: "Luật thế giới", rule: "Luật", boundary: "Ranh giới",
}

