package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"chinese-learn/internal/schedule"
	"chinese-learn/internal/store"
)

// ---------------- 复习计划 ----------------

type planItemJSON struct {
	HanziID     int64  `json:"hanzi_id"`
	Ch          string `json:"ch"`
	Reason      string `json:"reason"`
	ReasonLabel string `json:"reason_label"`
	Level       int    `json:"level"`
	IsNew       bool   `json:"is_new"`
}

type planJSON struct {
	Items       []planItemJSON `json:"items"`
	DueTotal    int            `json:"due_total"`
	NewTotal    int            `json:"new_total"`
	NewLeft     int            `json:"new_left"`
	Backlog     int            `json:"backlog"`
	Today       string         `json:"today"`
	PlannedSize int            `json:"planned_size"`
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	childID, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	plan, err := s.st.BuildPlan(childID, s.params, s.today())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "构造复习队列失败")
		return
	}

	out := planJSON{
		Items:       make([]planItemJSON, 0, len(plan.Items)),
		DueTotal:    plan.DueTotal,
		NewTotal:    plan.NewTotal,
		NewLeft:     plan.NewRemaining,
		Backlog:     plan.Backlog,
		Today:       string(plan.Day),
		PlannedSize: len(plan.Items),
	}
	for _, it := range plan.Items {
		out.Items = append(out.Items, planItemJSON{
			HanziID:     it.HanziID,
			Ch:          it.Ch,
			Reason:      it.Reason,
			ReasonLabel: it.ReasonLabel,
			Level:       it.Level,
			IsNew:       it.IsNew,
		})
	}

	s.writeJSON(w, http.StatusOK, out)
}

// ---------------- 提交复习 ----------------

type submitJSON struct {
	HanziID   int64  `json:"hanzi_id"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	LatencyMS int    `json:"latency_ms"`
}

type submitResultJSON struct {
	Level    int    `json:"level"`
	DueOn    string `json:"due_on"`
	Bound    string `json:"bound"`
	Lapsed   bool   `json:"lapsed"`
	Interval int    `json:"interval_days"`
}

func (s *Server) handleSubmitReview(w http.ResponseWriter, r *http.Request) {
	childID, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	var req submitJSON
	if err := s.readJSON(w, r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if req.HanziID == 0 {
		s.writeError(w, http.StatusBadRequest, "缺少 hanzi_id")
		return
	}

	var result schedule.Result
	switch req.Result {
	case "known":
		result = schedule.Known
	case "unknown":
		result = schedule.Unknown
	default:
		s.writeError(w, http.StatusBadRequest, "result 必须是 known 或 unknown")
		return
	}

	out, err := s.st.SubmitReview(s.params, store.SubmitRequest{
		ChildID:   childID,
		HanziID:   req.HanziID,
		Result:    result,
		SessionID: req.SessionID,
		LatencyMS: req.LatencyMS,
		Now:       s.today(),
	})
	if errors.Is(err, store.ErrDuplicate) {
		// 连点或双标签页：告诉前端忽略即可，不是错误。
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error": "这个字已经答过了", "code": "duplicate",
		})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "找不到这个字")
		return
	}
	if err != nil {
		s.log.Error("提交复习失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}

	s.writeJSON(w, http.StatusOK, submitResultJSON{
		Level:    out.Level,
		DueOn:    string(out.DueOn),
		Bound:    out.Bound,
		Lapsed:   out.Lapsed,
		Interval: out.IntervalDay,
	})
}

func (s *Server) handleUndo(w http.ResponseWriter, r *http.Request) {
	childID, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	id, result, err := s.st.UndoLast(childID)
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "没有可撤销的记录")
		return
	}
	if err != nil {
		s.log.Error("撤销失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "撤销失败")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"hanzi_id": id,
		"result":   result,
	})
}

// ---------------- 导入 ----------------

// importCache 暂存预览结果，避免让前端把全文回传两遍。
//
// 用内存缓存而不是落库：预览阶段不应产生任何持久化痕迹。
type importCache struct {
	mu    sync.Mutex
	items map[string]cachedImport
}

type cachedImport struct {
	Text      string
	ChildID   int64
	ExpiresAt time.Time
}

const importCacheTTL = 30 * time.Minute

func (c *importCache) put(token string, v cachedImport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = make(map[string]cachedImport)
	}
	// 顺手清理过期项，避免内存无限增长。
	now := time.Now()
	for k, item := range c.items {
		if now.After(item.ExpiresAt) {
			delete(c.items, k)
		}
	}
	c.items[token] = v
}

func (c *importCache) get(token string) (cachedImport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[token]
	if !ok || time.Now().After(v.ExpiresAt) {
		return cachedImport{}, false
	}
	return v, true
}

type importPreviewReq struct {
	SemesterID int64  `json:"semester_id"`
	SourceName string `json:"source_name"`
	Text       string `json:"text"`
}

type importPreviewResp struct {
	Token         string         `json:"token"`
	TotalRunes    int            `json:"total_runes"`
	UniqueHanzi   int            `json:"unique_hanzi"`
	AlreadyExists int            `json:"already_exists"`
	ToInsert      int            `json:"to_insert"`
	Skipped       map[string]int `json:"skipped"`
	Preview       string         `json:"preview"`
	Truncated     bool           `json:"truncated"`
}

func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	childID, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	var req importPreviewReq
	if err := s.readJSON(w, r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求格式不正确，内容可能过大")
		return
	}

	if msg := checkTextEncoding(req.Text); msg != "" {
		s.writeError(w, http.StatusBadRequest, msg)
		return
	}

	pv, err := s.st.PreviewImport(store.ImportRequest{
		ChildID: childID,
		Text:    req.Text,
	})
	if err != nil {
		s.log.Error("预览导入失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "预览失败")
		return
	}

	if pv.UniqueHanzi == 0 {
		s.writeError(w, http.StatusBadRequest,
			"没有从内容中识别出汉字。请确认粘贴的是中文内容。")
		return
	}

	token := randomToken()
	s.imports.put(token, cachedImport{
		Text:      req.Text,
		ChildID:   childID,
		ExpiresAt: time.Now().Add(importCacheTTL),
	})

	s.writeJSON(w, http.StatusOK, importPreviewResp{
		Token:         token,
		TotalRunes:    pv.TotalRunes,
		UniqueHanzi:   pv.UniqueHanzi,
		AlreadyExists: pv.AlreadyExists,
		ToInsert:      pv.ToInsert,
		Skipped:       pv.Skipped,
		Preview:       string(pv.Preview),
		Truncated:     pv.Truncated,
	})
}

type importCommitReq struct {
	Token      string `json:"token"`
	SemesterID int64  `json:"semester_id"`
	SourceName string `json:"source_name"`
}

func (s *Server) handleImportCommit(w http.ResponseWriter, r *http.Request) {
	childID, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	var req importCommitReq
	if err := s.readJSON(w, r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	cached, ok := s.imports.get(req.Token)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "预览已过期，请重新预览")
		return
	}
	if cached.ChildID != childID {
		s.writeError(w, http.StatusForbidden, "预览内容与当前孩子不符")
		return
	}

	if req.SemesterID == 0 {
		s.writeError(w, http.StatusBadRequest, "请先选择学期")
		return
	}

	_, err = s.st.CommitImport(store.ImportRequest{
		ChildID:    childID,
		SemesterID: req.SemesterID,
		SourceName: strings.TrimSpace(req.SourceName),
		SourceKind: "paste",
		Text:       cached.Text,
	})
	if err != nil {
		s.log.Error("导入失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "导入失败："+err.Error())
		return
	}

	// 导入后重新统计，让页面显示最新的数字。
	sem, serr := s.st.CurrentSemester(childID)
	var total int
	if serr == nil {
		s.st.DB().QueryRow(
			`SELECT COUNT(*) FROM hanzi WHERE semester_id = ?`, sem.ID).Scan(&total)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"semester_total": total,
	})
}

// checkTextEncoding 检查文本是否是有效的 UTF-8。
//
// 从 Windows 记事本另存为的 GBK 文件，中文会全部乱码、
// 一个字都抽不出来。与其让用户看到「0 个字」发懵，不如直接说清楚原因。
func checkTextEncoding(text string) string {
	if text == "" {
		return "内容不能为空"
	}
	if !utf8Valid(text) {
		return "内容不是 UTF-8 编码，中文会显示为乱码。" +
			"请用记事本打开原文件，另存为 UTF-8 编码后再导入。"
	}
	// zip / pdf / 旧版 Office 的魔数：这些是二进制格式，抽出的"汉字"是乱码。
	switch {
	case strings.HasPrefix(text, "PK\x03\x04"):
		return "这是 .docx / .xlsx 之类的压缩格式文件。请打开它，把文字复制出来粘贴。"
	case strings.HasPrefix(text, "%PDF"):
		return "这是 PDF 文件。请把里面的文字复制出来粘贴。"
	case strings.HasPrefix(text, "\xD0\xCF\x11\xE0"):
		return "这是旧版 Office 文件。请打开它，把文字复制出来粘贴。"
	}
	return ""
}

// ---------------- 学期 ----------------

type semesterReq struct {
	Name        string `json:"name"`
	MakeCurrent bool   `json:"make_current"`
	SemesterID  int64  `json:"semester_id"`
}

func (s *Server) handleCreateSemester(w http.ResponseWriter, r *http.Request) {
	childID, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	// 同时支持表单提交（页面下拉加「新建」）和 JSON。
	name, makeCurrent, ok := s.parseSemesterForm(r)
	if !ok {
		var req semesterReq
		if err := s.readJSON(w, r, &req); err != nil {
			s.writeError(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		name, makeCurrent = req.Name, req.MakeCurrent
	}

	name = strings.TrimSpace(name)
	if name == "" {
		s.writeError(w, http.StatusBadRequest, "学期名称不能为空")
		return
	}

	id, err := s.st.CreateSemester(childID, name, makeCurrent)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.writeError(w, http.StatusConflict, "已经有同名的学期了")
			return
		}
		s.log.Error("创建学期失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "创建失败")
		return
	}

	if s.wantsHTML(r) {
		http.Redirect(w, r, "/import?msg=created", http.StatusSeeOther)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) handleSetCurrentSemester(w http.ResponseWriter, r *http.Request) {
	childID, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	var semesterID int64
	if s.wantsHTML(r) {
		if err := r.ParseForm(); err != nil {
			s.writeError(w, http.StatusBadRequest, "表单格式不正确")
			return
		}
		semesterID, _ = parseInt64(r.FormValue("semester_id"))
	} else {
		var req semesterReq
		if err := s.readJSON(w, r, &req); err != nil {
			s.writeError(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		semesterID = req.SemesterID
	}

	if semesterID == 0 {
		s.writeError(w, http.StatusBadRequest, "缺少 semester_id")
		return
	}

	if err := s.st.SetCurrentSemester(childID, semesterID); err != nil {
		s.log.Error("切换学期失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "切换失败")
		return
	}

	if s.wantsHTML(r) {
		// 从哪来回哪去，带上提示。
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = "/"
		}
		http.Redirect(w, r, withMsg(ref, "switched"), http.StatusSeeOther)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteSemester(w http.ResponseWriter, r *http.Request) {
	_, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	var semesterID int64
	if s.wantsHTML(r) {
		if err := r.ParseForm(); err != nil {
			s.writeError(w, http.StatusBadRequest, "表单格式不正确")
			return
		}
		semesterID, _ = parseInt64(r.FormValue("semester_id"))
	} else {
		var req semesterReq
		if err := s.readJSON(w, r, &req); err != nil {
			s.writeError(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		semesterID = req.SemesterID
	}

	if err := s.st.DeleteSemester(semesterID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "学期不存在")
			return
		}
		s.log.Error("删除学期失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "删除失败")
		return
	}

	if s.wantsHTML(r) {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------- 字管理 ----------------

type hanziActionReq struct {
	Action string `json:"action"`
}

func (s *Server) handleHanziAction(w http.ResponseWriter, r *http.Request) {
	id, err := s.pathID(r, "id")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "缺少 id")
		return
	}

	var action string
	if s.wantsHTML(r) {
		if err := r.ParseForm(); err != nil {
			s.writeError(w, http.StatusBadRequest, "表单格式不正确")
			return
		}
		action = r.FormValue("action")
	} else {
		var req hanziActionReq
		if err := s.readJSON(w, r, &req); err != nil {
			s.writeError(w, http.StatusBadRequest, "请求格式不正确")
			return
		}
		action = req.Action
	}

	if err := s.st.SetHanziStatus(id, action, s.today()); err != nil {
		s.log.Error("修改字状态失败", "err", err, "action", action)
		s.writeError(w, http.StatusInternalServerError, "操作失败")
		return
	}

	if s.wantsHTML(r) {
		http.Redirect(w, r, "/hanzi/"+r.PathValue("id"), http.StatusSeeOther)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleHanziDelete(w http.ResponseWriter, r *http.Request) {
	id, err := s.pathID(r, "id")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "缺少 id")
		return
	}

	if err := s.st.DeleteHanzi(id); err != nil {
		s.log.Error("删除字失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "删除失败")
		return
	}

	if s.wantsHTML(r) {
		http.Redirect(w, r, "/hanzi", http.StatusSeeOther)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------- 设置 ----------------

type settingsReq struct {
	DailyNewCap    int `json:"daily_new_cap"`
	DailyReviewCap int `json:"daily_review_cap"`
	DayCutoffHour  int `json:"day_cutoff_hour"`
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	childID, err := s.currentChildID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取孩子档案失败")
		return
	}

	cur, err := s.st.GetSettings(childID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "读取设置失败")
		return
	}

	if s.wantsHTML(r) {
		if err := r.ParseForm(); err != nil {
			s.writeError(w, http.StatusBadRequest, "表单格式不正确")
			return
		}

		// 无效输入要报错，不能静默保持旧值——否则用户以为改成功了。
		newCap, err := parseSettingValue(r.FormValue("daily_new_cap"), cur.DailyNewCap)
		if err != nil {
			http.Redirect(w, r, withMsg("/settings", "badvalue"), http.StatusSeeOther)
			return
		}
		reviewCap, err := parseSettingValue(r.FormValue("daily_review_cap"), cur.DailyReviewCap)
		if err != nil {
			http.Redirect(w, r, withMsg("/settings", "badvalue"), http.StatusSeeOther)
			return
		}
		cutoffHour, err := parseSettingValue(r.FormValue("day_cutoff_hour"), cur.DayCutoffHour)
		if err != nil {
			http.Redirect(w, r, withMsg("/settings", "badvalue"), http.StatusSeeOther)
			return
		}

		cur.DailyNewCap = clamp(newCap, 0, maxSettingValue)
		cur.DailyReviewCap = clamp(reviewCap, 1, maxSettingValue)
		cur.DayCutoffHour = clamp(cutoffHour, 0, 23)

		if err := s.st.SaveSettings(childID, cur); err != nil {
			s.log.Error("保存设置失败", "err", err)
			s.writeError(w, http.StatusInternalServerError, "保存失败")
			return
		}
		// 日切点变了，下次请求生效。
		s.st.SetCutoff(cur.Cutoff())
		// 用查询参数带一个提示回设置页（PRG 模式）：
		// 刷新页面不会重复提交，提示也不会一直挂着。
		http.Redirect(w, r, withMsg("/settings", "settings"), http.StatusSeeOther)
		return
	}

	var req settingsReq
	if err := s.readJSON(w, r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	cur.DailyNewCap = clamp(req.DailyNewCap, 0, maxSettingValue)
	cur.DailyReviewCap = clamp(req.DailyReviewCap, 1, maxSettingValue)
	cur.DayCutoffHour = clamp(req.DayCutoffHour, 0, 23)

	if err := s.st.SaveSettings(childID, cur); err != nil {
		s.log.Error("保存设置失败", "err", err)
		s.writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	s.st.SetCutoff(cur.Cutoff())

	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// maxSettingValue 是数量类设置的防呆上限，取自 store 的单一来源。
const maxSettingValue = store.MaxSettingValue

// ---------------- 助手 ----------------

// flashMessages 把 flash 代码映射成给人看的提示。
//
// 用查询参数而不是 session/cookie：家用场景没有会话，
// 一个参数就够，且刷新页面后提示自然消失，不会一直挂着。
var flashMessages = map[string]string{
	"settings": "设置已保存",
	"switched": "已切换学期。新字从新学期开始学，以前没掌握的字仍然会继续复习。",
	"created":  "学期已创建",
	"deleted":  "学期已删除",
	"badvalue": "请输入数字。刚才的改动没有保存。",
}

// withMsg 给跳转地址追加 flash 提示参数。
func withMsg(base, code string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("msg", code)
	u.RawQuery = q.Encode()
	return u.String()
}

// flashErrorCodes 是那些本质上是「操作没成功」的提示，
// 前端会用不同的配色显示。
var flashErrorCodes = map[string]bool{"badvalue": true}

// flashFrom 从请求里取出要显示的提示文字和它是否是错误。
//
// 查不到代码时返回空串而不是原样显示——宁愿不显示，
// 也不要把内部代码暴露给用户。
func flashFrom(r *http.Request) (text string, isError bool) {
	code := r.URL.Query().Get("msg")
	return flashMessages[code], flashErrorCodes[code]
}

// wantsHTML 判断这个请求期望 HTML 响应（表单提交）还是 JSON。
func (s *Server) wantsHTML(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(ct, "multipart/form-data")
}

// parseSemesterForm 从表单里读学期信息。返回 ok=false 表示不是表单请求。
func (s *Server) parseSemesterForm(r *http.Request) (name string, makeCurrent, ok bool) {
	if !s.wantsHTML(r) {
		return "", false, false
	}
	if err := r.ParseForm(); err != nil {
		return "", false, false
	}
	name = r.FormValue("name")
	makeCurrent = r.FormValue("make_current") == "1"
	return name, makeCurrent, true
}

func parseInt64(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseSettingValue 解析一个设置项。
//
// 空字符串视为「没改」，返回 def；其他无法解析的输入返回错误，
// 让调用方给用户一个明确提示，而不是静默保留旧值。
//
// 用 strconv 而不是手写循环：手写版本在超大输入时会整数溢出
// （例如 99999999999 会变成负数），strconv 会直接报错。
func parseSettingValue(s string, def int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def, errors.New("请输入一个数字")
	}
	return n, nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
