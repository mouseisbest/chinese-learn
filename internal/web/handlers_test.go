package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"chinese-learn/internal/schedule"
	"chinese-learn/internal/store"
)

// newTestServer 起一个用临时库的测试服务器。
func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()

	dir := t.TempDir()
	st, err := store.Open(store.Options{
		Path:     filepath.Join(dir, "test.db"),
		Location: time.FixedZone("CST", 8*3600),
		Cutoff:   4 * time.Hour,
	})
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.EnsureSeed(); err != nil {
		t.Fatalf("初始化: %v", err)
	}

	srv, err := New(Options{
		Store:  st,
		Params: schedule.DefaultParams(),
		Dev:    false,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("构造服务器: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// postJSON 发一个 JSON POST 请求。
func postJSON(t *testing.T, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if s, ok := body.(string); ok {
		r = strings.NewReader(s)
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = strings.NewReader(string(b))
	}

	resp, err := http.Post(url, "application/json", r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func TestRoutes_NotFound(t *testing.T) {
	ts, _ := newTestServer(t)

	// Go 1.22 的 "GET /" 是子树通配，会把所有未匹配路径都吃掉。
	// 注册时必须写 "GET /{$}"，否则这里会返回 200 而不是 404。
	resp, err := http.Get(ts.URL + "/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("不存在的路径返回 %d, want 404（检查 \"GET /{$}\" 是否写对）",
			resp.StatusCode)
	}
}

func TestPages_Render(t *testing.T) {
	ts, _ := newTestServer(t)

	for _, path := range []string{"/", "/review", "/import", "/hanzi", "/settings"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("状态码 = %d, want 200", resp.StatusCode)
			}

			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(body), "页面渲染失败") {
				t.Error("模板渲染失败")
			}
			if len(body) < 200 {
				t.Errorf("页面内容过短（%d 字节），可能渲染出错", len(body))
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("healthz = %q, want ok", string(body))
	}
}

func TestImportFlow(t *testing.T) {
	ts, _ := newTestServer(t)

	// ---- 预览 ----
	resp, body := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1,
		"source_name": "第一课",
		"text":        "天地人，日月星。一二〇。",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("预览状态码 = %d, body=%s", resp.StatusCode, body)
	}

	var pv importPreviewResp
	if err := json.Unmarshal(body, &pv); err != nil {
		t.Fatal(err)
	}

	// 「天地人，日月星。一二〇。」→ 8 个汉字。
	// 标点和 〇 必须被排除（〇 是 U+3007，看着像汉字但不是）。
	if pv.UniqueHanzi != 8 {
		t.Errorf("抽出字数 = %d, want 8（〇 和标点不应被算入）", pv.UniqueHanzi)
	}
	if pv.ToInsert != 8 {
		t.Errorf("将新增 = %d, want 8", pv.ToInsert)
	}
	if strings.ContainsRune(pv.Preview, '〇') {
		t.Error("预览里不应出现 〇")
	}
	if pv.Skipped["symbol"] == 0 {
		t.Error("〇 应被归类为符号")
	}

	// ---- 确认导入 ----
	resp, body = postJSON(t, ts.URL+"/api/import/commit", map[string]any{
		"token": pv.Token, "semester_id": 1, "source_name": "第一课",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导入状态码 = %d, body=%s", resp.StatusCode, body)
	}

	var done map[string]any
	json.Unmarshal(body, &done)
	if done["ok"] != true {
		t.Errorf("导入应返回 ok=true, 得到 %v", done)
	}
}

func TestImport_PreviewDoesNotWrite(t *testing.T) {
	ts, st := newTestServer(t)

	// 预览后立即查库，一个字都不应该有。
	postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1, "text": "天地人日月星",
	})

	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM hanzi`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("预览后库里已有 %d 个字，预览不应写库", n)
	}
}

func TestImport_RejectsGarbage(t *testing.T) {
	ts, _ := newTestServer(t)

	cases := []struct {
		name string
		text string
	}{
		{"纯英文", "hello world foo bar"},
		{"纯数字", "1234567890"},
		{"空内容", "   "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, _ := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
				"semester_id": 1, "text": c.text,
			})
			if resp.StatusCode == http.StatusOK {
				t.Errorf("应拒绝 %q，但返回了 200", c.text)
			}
		})
	}
}

func TestImport_RejectsBinaryFormat(t *testing.T) {
	ts, _ := newTestServer(t)

	// .docx 是 zip，直接粘贴会出现 PK 魔数。
	resp, body := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1, "text": "PK\x03\x04天地人",
	})
	if resp.StatusCode == http.StatusOK {
		t.Error("应拒绝二进制格式文件")
	}
	if !strings.Contains(string(body), "docx") && !strings.Contains(string(body), "复制") {
		t.Errorf("错误提示应说清楚怎么处理，得到: %s", body)
	}
}

func TestImport_CommitWithoutPreview(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, _ := postJSON(t, ts.URL+"/api/import/commit", map[string]any{
		"token": "bogus-token", "semester_id": 1,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("无效 token 应返回 400, 得到 %d", resp.StatusCode)
	}
}

func TestReviewFlow(t *testing.T) {
	ts, st := newTestServer(t)

	// 导入并让当天配额足够。
	postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1, "text": "天地人",
	})
	resp, body := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1, "text": "天地人",
	})
	var pv importPreviewResp
	json.Unmarshal(body, &pv)
	postJSON(t, ts.URL+"/api/import/commit", map[string]any{
		"token": pv.Token, "semester_id": 1, "source_name": "t",
	})

	// ---- 取计划 ----
	resp, body = postJSON(t, ts.URL+"/api/session/plan", "{}")
	if resp.StatusCode != http.StatusOK {
		// 计划接口是 GET，改用正确方法。
		r2, err := http.Get(ts.URL + "/api/session/plan")
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(r2.Body)
		r2.Body.Close()
		_ = body
	}

	r3, err := http.Get(ts.URL + "/api/session/plan")
	if err != nil {
		t.Fatal(err)
	}
	planBody, _ := io.ReadAll(r3.Body)
	r3.Body.Close()

	var plan planJSON
	if err := json.Unmarshal(planBody, &plan); err != nil {
		t.Fatalf("解析计划失败: %v, body=%s", err, planBody)
	}
	if len(plan.Items) == 0 {
		t.Fatal("计划里应有新字")
	}

	item := plan.Items[0]

	// ---- 不认识 → 明天必看 ----
	resp, body = postJSON(t, ts.URL+"/api/review", map[string]any{
		"hanzi_id": item.HanziID, "result": "unknown",
		"session_id": "s1", "latency_ms": 1000,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("提交状态码 = %d, body=%s", resp.StatusCode, body)
	}

	var out submitResultJSON
	json.Unmarshal(body, &out)
	if out.Level != 1 {
		t.Errorf("不认识后等级 = %d, want 1", out.Level)
	}

	// 到期日应是明天（用服务器的逻辑日算）。
	var dueOn string
	if err := st.DB().QueryRow(
		`SELECT due_on FROM review_state WHERE hanzi_id = ?`, item.HanziID).Scan(&dueOn); err != nil {
		t.Fatal(err)
	}
	if dueOn == "" {
		t.Error("不认识后应有到期日")
	}

	// ---- 连点应被 409 挡住 ----
	resp, _ = postJSON(t, ts.URL+"/api/review", map[string]any{
		"hanzi_id": item.HanziID, "result": "known",
		"session_id": "s1", "latency_ms": 50,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("重复提交应返回 409, 得到 %d", resp.StatusCode)
	}
}

func TestReview_InvalidResult(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, _ := postJSON(t, ts.URL+"/api/review", map[string]any{
		"hanzi_id": 1, "result": "maybe",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 result 应返回 400, 得到 %d", resp.StatusCode)
	}
}

func TestReview_Undo(t *testing.T) {
	ts, st := newTestServer(t)

	// 准备一个字。
	resp, body := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1, "text": "天地人",
	})
	var pv importPreviewResp
	json.Unmarshal(body, &pv)
	postJSON(t, ts.URL+"/api/import/commit", map[string]any{
		"token": pv.Token, "semester_id": 1, "source_name": "t",
	})

	r, _ := http.Get(ts.URL + "/api/session/plan")
	planBody, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var plan planJSON
	json.Unmarshal(planBody, &plan)

	item := plan.Items[0]
	subResp, subBody := postJSON(t, ts.URL+"/api/review", map[string]any{
		"hanzi_id": item.HanziID, "result": "unknown",
	})
	if subResp.StatusCode != http.StatusOK {
		t.Fatalf("提交失败: %d %s", subResp.StatusCode, subBody)
	}

	// ---- 撤销 ----
	resp, body = postJSON(t, ts.URL+"/api/review/undo", "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("撤销状态码 = %d, body=%s", resp.StatusCode, body)
	}

	var undone map[string]any
	json.Unmarshal(body, &undone)
	if undone["result"] != "unknown" {
		t.Errorf("撤销返回的结果 = %v, want unknown", undone["result"])
	}

	// 状态应回到未学。
	var level int
	var dueOn *string
	if err := st.DB().QueryRow(
		`SELECT level, due_on FROM review_state WHERE hanzi_id = ?`, item.HanziID).
		Scan(&level, &dueOn); err != nil {
		t.Fatal(err)
	}
	if level != 0 || dueOn != nil {
		t.Errorf("撤销后 level=%d due=%v, want 0/nil", level, dueOn)
	}
}

func TestSemester_CreateAndSwitch(t *testing.T) {
	ts, st := newTestServer(t)

	// 建第二个学期。
	resp, body := postJSON(t, ts.URL+"/api/semesters", map[string]any{
		"name": "一年级下册", "make_current": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("建学期失败: %d %s", resp.StatusCode, body)
	}

	var created map[string]any
	json.Unmarshal(body, &created)
	newID := int64(created["id"].(float64))

	// 当前学期应已切过去。
	var cur int64
	if err := st.DB().QueryRow(
		`SELECT id FROM semester WHERE is_current = 1`).Scan(&cur); err != nil {
		t.Fatal(err)
	}
	if cur != newID {
		t.Errorf("当前学期 = %d, want %d", cur, newID)
	}

	// 同名不允许重复建。
	resp, _ = postJSON(t, ts.URL+"/api/semesters", map[string]any{
		"name": "一年级下册",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("重名应返回 409, 得到 %d", resp.StatusCode)
	}
}

func TestSemester_SwitchKeepsOldDueCards(t *testing.T) {
	ts, st := newTestServer(t)

	// 上学期导入并让一个字到期。
	resp, body := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1, "text": "天地人",
	})
	var pv importPreviewResp
	json.Unmarshal(body, &pv)
	postJSON(t, ts.URL+"/api/import/commit", map[string]any{
		"token": pv.Token, "semester_id": 1, "source_name": "t",
	})
	_ = resp

	// 手工把它设为已学且逾期。
	var hid int64
	st.DB().QueryRow(`SELECT id FROM hanzi WHERE ch = '天'`).Scan(&hid)
	if _, err := st.DB().Exec(
		`UPDATE review_state SET level = 2, first_learned_on = '2026-09-01',
		   last_review_on = '2026-09-10', due_on = '2026-09-15' WHERE hanzi_id = ?`,
		hid); err != nil {
		t.Fatal(err)
	}

	// 建下学期并切过去，导入新字。
	resp, body = postJSON(t, ts.URL+"/api/semesters", map[string]any{
		"name": "一年级下册", "make_current": true,
	})
	var created map[string]any
	json.Unmarshal(body, &created)
	sem2 := int64(created["id"].(float64))

	postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": sem2, "text": "春夏秋冬",
	})
	resp2, body2 := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": sem2, "text": "春夏秋冬",
	})
	var pv2 importPreviewResp
	json.Unmarshal(body2, &pv2)
	postJSON(t, ts.URL+"/api/import/commit", map[string]any{
		"token": pv2.Token, "semester_id": sem2, "source_name": "t2",
	})
	_ = resp2

	// ---- 队列应同时包含上学期的逾期字 + 下学期的新字 ----
	r, err := http.Get(ts.URL + "/api/session/plan")
	if err != nil {
		t.Fatal(err)
	}
	planBody, _ := io.ReadAll(r.Body)
	r.Body.Close()

	var plan planJSON
	json.Unmarshal(planBody, &plan)

	var hasOld, hasNew bool
	for _, it := range plan.Items {
		if it.Ch == "天" && !it.IsNew {
			hasOld = true
		}
		if it.Ch == "春" && it.IsNew {
			hasNew = true
		}
	}
	if !hasOld {
		t.Error("★ 上学期未掌握的字「天」应仍在复习队列里")
	}
	if !hasNew {
		t.Error("新字应从当前学期（下册）放出")
	}
}

func TestSettings_Save(t *testing.T) {
	ts, st := newTestServer(t)

	resp, body := postJSON(t, ts.URL+"/api/settings", map[string]any{
		"daily_new_cap": 3, "daily_review_cap": 12, "day_cutoff_hour": 5,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存设置失败: %d %s", resp.StatusCode, body)
	}

	out, err := st.GetSettings(1)
	if err != nil {
		t.Fatal(err)
	}
	if out.DailyNewCap != 3 || out.DailyReviewCap != 12 || out.DayCutoffHour != 5 {
		t.Errorf("设置 = %+v, want 3/12/5", out)
	}
}

func TestHanziPage_Filters(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1, "text": "天地人日月星",
	})
	var pv importPreviewResp
	json.Unmarshal(body, &pv)
	postJSON(t, ts.URL+"/api/import/commit", map[string]any{
		"token": pv.Token, "semester_id": 1, "source_name": "t",
	})
	_ = resp

	for _, f := range []string{"all", "due", "new", "mastered", "wrong"} {
		t.Run(f, func(t *testing.T) {
			r, err := http.Get(ts.URL + "/hanzi?filter=" + f)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if r.StatusCode != http.StatusOK {
				t.Errorf("filter=%s 状态码 = %d", f, r.StatusCode)
			}
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), "页面渲染失败") {
				t.Errorf("filter=%s 渲染失败", f)
			}
		})
	}
}

// 导入超长内容不应把内存打满，也不应报错。
func TestImport_LongText(t *testing.T) {
	ts, _ := newTestServer(t)

	var sb strings.Builder
	for i := 0; i < 20000; i++ {
		sb.WriteString("天地人日月星")
	}

	resp, body := postJSON(t, ts.URL+"/api/import/preview", map[string]any{
		"semester_id": 1, "text": sb.String(),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("超长文本应能处理: %d %s", resp.StatusCode, body)
	}

	var pv importPreviewResp
	json.Unmarshal(body, &pv)
	// 重复内容应被去重成 6 个字。
	if pv.UniqueHanzi != 6 {
		t.Errorf("去重后 = %d, want 6", pv.UniqueHanzi)
	}
}

// postFormNoRedirect 提交表单但不跟随重定向。
//
// http.Post 用 DefaultClient，会自动跟随 303——那样就拿不到
// Location 头，也看不到原始状态码。测 PRG 模式必须禁用跟随。
func postFormNoRedirect(t *testing.T, url, form string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(form))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// 操作成功后的横幅提示。
func TestFlash_AfterSaveSettings(t *testing.T) {
	ts, _ := newTestServer(t)

	// 表单提交应重定向到带 msg 的地址（PRG 模式）。
	form := "daily_new_cap=7&daily_review_cap=15&day_cutoff_hour=4"
	resp := postFormNoRedirect(t, ts.URL+"/api/settings", form)
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("状态码 = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "msg=") {
		t.Errorf("跳转地址应带 msg 参数，得到 %q", loc)
	}

	// 带参数访问设置页，应显示提示。
	r2, err := http.Get(ts.URL + "/settings?msg=settings")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	body, _ := io.ReadAll(r2.Body)

	if !strings.Contains(string(body), "设置已保存") {
		t.Error("设置页应显示「设置已保存」横幅")
	}
	if !strings.Contains(string(body), "flash-banner") {
		t.Error("应有 flash-banner 元素")
	}

	// 不带参数时不该出现。
	r3, _ := http.Get(ts.URL + "/settings")
	body3, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if strings.Contains(string(body3), "flash-banner") {
		t.Error("无 msg 参数时不应显示横幅")
	}
}

// 未知的 msg 代码必须被忽略，不能把内部代码显示给用户。
func TestFlash_IgnoresUnknownCode(t *testing.T) {
	ts, _ := newTestServer(t)

	for _, code := range []string{"hacked", "<script>alert(1)</script>", "1=1"} {
		u := ts.URL + "/settings?msg=" + url.QueryEscape(code)
		r, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		if strings.Contains(string(body), "flash-banner") {
			t.Errorf("未知代码 %q 不应产生横幅", code)
		}
		// 更要紧的是：不能把输入原样回显到页面上。
		if code != "1=1" && strings.Contains(string(body), code) {
			t.Errorf("未知代码 %q 被回显到页面上了", code)
		}
	}
}

// 切换学期应带提示。
func TestFlash_AfterSemesterSwitch(t *testing.T) {
	ts, st := newTestServer(t)

	id, err := st.CreateSemester(1, "一年级下册", false)
	if err != nil {
		t.Fatal(err)
	}

	resp := postFormNoRedirect(t, ts.URL+"/api/semesters/current",
		"semester_id="+strconv.FormatInt(id, 10))
	resp.Body.Close()

	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "msg=") {
		t.Errorf("切换学期后跳转地址应带 msg，得到 %q", loc)
	}
}

// 设置项的上限：大到等于不限制，但挡住手滑输入天文数字。
func TestSettings_LargeValues(t *testing.T) {
	ts, st := newTestServer(t)

	cases := []struct {
		input string
		want  int
		desc  string
	}{
		{"50", 50, "正常值"},
		{"999", 999, "旧上限 100 之外"},
		{"10000", 10000, "新上限"},
		{"10001", 10000, "超上限被夹住"},
		{"99999999999", 10000, "极大值不溢出成负数"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			resp := postFormNoRedirect(t, ts.URL+"/api/settings",
				"daily_new_cap="+c.input+"&daily_review_cap=25&day_cutoff_hour=4")
			resp.Body.Close()

			got, err := st.GetSettings(1)
			if err != nil {
				t.Fatal(err)
			}
			if got.DailyNewCap != c.want {
				t.Errorf("输入 %s 保存为 %d, want %d", c.input, got.DailyNewCap, c.want)
			}
			if got.DailyNewCap < 0 {
				t.Errorf("输入 %s 变成了负数 %d（整数溢出）", c.input, got.DailyNewCap)
			}
		})
	}
}

// 无效输入必须明确报错，不能静默保留旧值冒充成功。
func TestSettings_InvalidValueReportsError(t *testing.T) {
	ts, st := newTestServer(t)

	// 先设一个已知值。
	postFormNoRedirect(t, ts.URL+"/api/settings",
		"daily_new_cap=9&daily_review_cap=25&day_cutoff_hour=4").Body.Close()

	for _, bad := range []string{"abc", "-5", "7.5"} {
		t.Run(bad, func(t *testing.T) {
			resp := postFormNoRedirect(t, ts.URL+"/api/settings",
				"daily_new_cap="+bad+"&daily_review_cap=25&day_cutoff_hour=4")
			resp.Body.Close()

			if loc := resp.Header.Get("Location"); !strings.Contains(loc, "badvalue") {
				t.Errorf("无效输入应提示 badvalue，跳转到了 %q", loc)
			}

			// 值不能被改动。
			got, _ := st.GetSettings(1)
			if got.DailyNewCap != 9 {
				t.Errorf("无效输入 %q 改动了设置: %d, want 9", bad, got.DailyNewCap)
			}
		})
	}
}

// HTML 页面不能带缓存，否则用户保存后可能看到旧值。
func TestPages_NoCacheHeaders(t *testing.T) {
	ts, _ := newTestServer(t)

	for _, path := range []string{"/", "/settings", "/hanzi", "/import"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			cc := resp.Header.Get("Cache-Control")
			if !strings.Contains(cc, "no-store") {
				t.Errorf("%s 的 Cache-Control = %q, 应包含 no-store", path, cc)
			}
		})
	}
}
