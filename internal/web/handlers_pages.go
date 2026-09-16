package web

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"

	"chinese-learn/internal/store"
)

// navData 是每个页面都需要的导航信息。
type navData struct {
	Title             string
	BodyClass         string
	ChildName         string
	Semesters         []store.Semester
	CurrentSemesterID int64
	// Flash 是操作结果的横幅提示，空串表示不显示。
	Flash string
	// FlashIsError 为真时横幅用错误配色（红），否则用成功配色（绿）。
	FlashIsError bool
	// ErrorMsg 仅在错误页使用。
	ErrorMsg string
}

// baseData 组装导航公共数据。
//
// r 用于读取 flash 提示参数；传 nil 表示不需要提示。
func (s *Server) baseData(r *http.Request, title, bodyClass string) (navData, int64, error) {
	childID, err := s.currentChildID()
	if err != nil {
		return navData{}, 0, err
	}

	child, err := s.st.GetChild(childID)
	if err != nil {
		return navData{}, 0, err
	}

	sems, err := s.st.ListSemesters(childID)
	if err != nil {
		return navData{}, 0, err
	}

	d := navData{
		Title:     title,
		BodyClass: bodyClass,
		ChildName: child.Name,
		Semesters: sems,
	}
	if r != nil {
		d.Flash, d.FlashIsError = flashFrom(r)
	}
	if cur, err := s.st.CurrentSemester(childID); err == nil {
		d.CurrentSemesterID = cur.ID
	}

	return d, childID, nil
}

// ---------------- 仪表盘 ----------------

type dashboardData struct {
	navData
	Stats         store.OverallStats
	SemesterStats []store.SemesterStats
	Heat          []store.HeatCell
	Today         string
	PlannedCount  int
	NewRemaining  int
	HasAnyHanzi   bool
	BacklogNote   string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	base, childID, err := s.baseData(r, "认字练习", "page-dashboard")
	if err != nil {
		s.renderError(w, err)
		return
	}

	today := s.today()

	stats, err := s.st.ChildStats(childID, today)
	if err != nil {
		s.renderError(w, err)
		return
	}

	semStats, err := s.st.SemesterStatsList(childID, today)
	if err != nil {
		s.renderError(w, err)
		return
	}

	heat, err := s.st.Heatmap(childID, today, 7)
	if err != nil {
		s.renderError(w, err)
		return
	}

	// 构造一次队列，只为知道今天计划和剩余配额。
	plan, err := s.st.BuildPlan(childID, s.params, today)
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := dashboardData{
		navData:       base,
		Stats:         stats,
		SemesterStats: semStats,
		Heat:          heat,
		Today:         string(today),
		PlannedCount:  len(plan.Items),
		NewRemaining:  plan.NewRemaining,
		HasAnyHanzi:   stats.Total > 0,
	}

	// 积压提示：温和陈述，不用红色警告——这是给家长的辅助工具，不是打卡 KPI。
	if plan.Backlog > 0 {
		cap := s.settingsFor(childID).DailyReviewCap
		if cap < 1 {
			cap = 1
		}
		days := plan.Backlog/cap + 1
		data.BacklogNote = "有 " + strconv.Itoa(plan.Backlog) +
			" 个字在等你。每天做 " + strconv.Itoa(cap) +
			" 个，大约 " + strconv.Itoa(days) + " 天就能追上。"
	}

	s.renderPage(w, "dashboard.html", data)
}

// settingsFor 读设置，失败时用默认值（不阻断页面）。
func (s *Server) settingsFor(childID int64) store.Settings {
	cfg, err := s.st.GetSettings(childID)
	if err != nil {
		return store.DefaultSettings()
	}
	return cfg
}

// ---------------- 复习页 ----------------

type reviewData struct {
	navData
	ChildID     int64
	Empty       bool
	EmptyTitle  string
	EmptyHint   string
	PlannedSize int
}

func (s *Server) handleReviewPage(w http.ResponseWriter, r *http.Request) {
	base, childID, err := s.baseData(r, "认字练习", "page-review")
	if err != nil {
		s.renderError(w, err)
		return
	}

	plan, err := s.st.BuildPlan(childID, s.params, s.today())
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := reviewData{
		navData:     base,
		ChildID:     childID,
		PlannedSize: len(plan.Items),
	}

	if len(plan.Items) == 0 {
		data.Empty = true
		data.EmptyTitle, data.EmptyHint = s.emptyReason(childID, plan)
	}

	s.renderPage(w, "review.html", data)
}

// emptyReason 说明队列为空的原因。
//
// 把原因说清楚很重要：用户看到空页面时的第一反应是「是不是坏了」，
// 而实际上往往是「今天的配额用完了」这种正常情况。
func (s *Server) emptyReason(childID int64, plan store.Plan) (title, hint string) {
	cfg := s.settingsFor(childID)

	// 有字可学但今天没放出来 → 配额用完。
	if plan.NewTotal > 0 && plan.NewRemaining == 0 {
		return "今天的新字学完了", "每天最多学 " + itoa(cfg.DailyNewCap) +
			" 个新字，剩下 " + itoa(plan.NewTotal) + " 个明天再学。"
	}
	// 有到期的字但没排进来 → 不太可能，但说清楚总比沉默好。
	if plan.DueTotal > 0 {
		return "今天的字都复习完了", "明天会有 " + itoa(plan.DueTotal) + " 个字要复习。"
	}
	// 真的没有任何字。
	if plan.NewTotal == 0 {
		return "还没有字可以学", "去「导入」页把生字加进来吧。"
	}
	return "今天没有要复习的字", "明天再来看看吧。"
}

func itoa(n int) string { return strconv.Itoa(n) }

// ---------------- 导入页 ----------------

type importData struct {
	navData
	ChildID      int64
	Batches      []store.BatchInfo
	HasSemesters bool
}

func (s *Server) handleImportPage(w http.ResponseWriter, r *http.Request) {
	base, childID, err := s.baseData(r, "导入生字", "page-import")
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := importData{
		navData:      base,
		ChildID:      childID,
		HasSemesters: len(base.Semesters) > 0,
	}

	if base.CurrentSemesterID != 0 {
		batches, err := s.st.ListBatches(base.CurrentSemesterID)
		if err == nil {
			data.Batches = batches
		}
	}

	s.renderPage(w, "import.html", data)
}

// ---------------- 字表页 ----------------

type hanziData struct {
	navData
	ChildID int64
	Rows    []store.HanziRow
	Total   int
	Filter  string
	Page    int
	Pages   int
	Today   string
}

const hanziPageSize = 120

func (s *Server) handleHanziPage(w http.ResponseWriter, r *http.Request) {
	base, childID, err := s.baseData(r, "字表", "page-hanzi")
	if err != nil {
		s.renderError(w, err)
		return
	}

	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	today := s.today()
	rows, total, err := s.st.ListHanzi(childID, today, filter, hanziPageSize, (page-1)*hanziPageSize)
	if err != nil {
		s.renderError(w, err)
		return
	}

	pages := (total + hanziPageSize - 1) / hanziPageSize
	if pages < 1 {
		pages = 1
	}

	s.renderPage(w, "hanzi.html", hanziData{
		navData: base,
		ChildID: childID,
		Rows:    rows,
		Total:   total,
		Filter:  filter,
		Page:    page,
		Pages:   pages,
		Today:   string(today),
	})
}

type hanziDetailData struct {
	navData
	Row     store.HanziRow
	History []store.LogRow
}

func (s *Server) handleHanziDetailPage(w http.ResponseWriter, r *http.Request) {
	base, _, err := s.baseData(r, "字详情", "page-hanzi-detail")
	if err != nil {
		s.renderError(w, err)
		return
	}

	id, err := s.pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}

	row, err := s.st.HanziDetail(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, err)
		return
	}

	history, err := s.st.HanziHistory(id, 50)
	if err != nil {
		s.renderError(w, err)
		return
	}

	s.renderPage(w, "hanzi_detail.html", hanziDetailData{
		navData: base,
		Row:     row,
		History: history,
	})
}

// ---------------- 设置页 ----------------

type settingsData struct {
	navData
	ChildID  int64
	Settings store.Settings
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	base, childID, err := s.baseData(r, "设置", "page-settings")
	if err != nil {
		s.renderError(w, err)
		return
	}

	st, err := s.st.GetSettings(childID)
	if err != nil {
		s.renderError(w, err)
		return
	}

	s.renderPage(w, "settings.html", settingsData{
		navData:  base,
		ChildID:  childID,
		Settings: st,
	})
}

// renderError 把后端错误转成一个友好的错误页。
func (s *Server) renderError(w http.ResponseWriter, err error) {
	s.log.Error("页面处理失败", "err", err)

	msg := "出了点问题。请刷新页面重试。"
	if s.dev {
		msg = err.Error() // 开发时直接显示原因，便于排查
	}

	data := navData{Title: "出错了", BodyClass: "page-error", ErrorMsg: msg}

	// 先渲染到缓冲区，成功后再写 header——避免渲染失败时
	// 已经发出 500 状态码却又写不出内容。
	var buf bytes.Buffer
	if err := s.renderer.tpl.ExecuteTemplate(&buf, "error.html", data); err != nil {
		s.log.Error("渲染错误页失败", "err", err)
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	w.Write(buf.Bytes())
}
