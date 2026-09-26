// Package web 提供 HTTP 接口与页面。
package web

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"chinese-learn/internal/day"
	"chinese-learn/internal/schedule"
	"chinese-learn/internal/store"
)

// Server 组装路由与依赖。
type Server struct {
	st       *store.Store
	params   schedule.Params
	renderer *renderer
	log      *slog.Logger
	// dev 模式下模板与静态资源从磁盘读取，改完刷新即见。
	dev bool
	// assetsDir 是 dev 模式下的静态资源目录。
	assetsDir string
	// imports 暂存导入预览结果，确认后才写库。
	imports importCache
}

// Options 是构造 Server 的参数。
type Options struct {
	Store     *store.Store
	Params    schedule.Params
	Dev       bool
	Templates string // 模板目录（dev 模式）
	StaticDir string // 静态资源目录（dev 模式）
	Logger    *slog.Logger
}

// New 构造 Server 并注册全部路由。
//
// 路由集中在此注册：Go 1.22 的 ServeMux 在模式冲突时会 panic，
// 集中注册便于一眼看出冲突，也避免散落在 init() 里。
func New(opt Options) (*Server, error) {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Params.Ladder == nil {
		opt.Params = schedule.DefaultParams()
	}

	r, err := newRenderer(opt.Dev, opt.Templates)
	if err != nil {
		return nil, err
	}

	s := &Server{
		st:        opt.Store,
		params:    opt.Params,
		renderer:  r,
		log:       opt.Logger,
		dev:       opt.Dev,
		assetsDir: opt.StaticDir,
	}
	return s, nil
}

// Handler 返回配置好的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- 页面 ----
	// 注意 {$}：没有它，"GET /" 会匹配所有未注册的路径，
	// 导致不存在的地址返回首页而不是 404。
	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /review", s.handleReviewPage)
	mux.HandleFunc("GET /import", s.handleImportPage)
	mux.HandleFunc("GET /hanzi", s.handleHanziPage)
	mux.HandleFunc("GET /hanzi/{id}", s.handleHanziDetailPage)
	mux.HandleFunc("GET /settings", s.handleSettingsPage)

	// ---- API ----
	mux.HandleFunc("GET /api/session/plan", s.handlePlan)
	mux.HandleFunc("POST /api/review", s.handleSubmitReview)
	mux.HandleFunc("POST /api/review/undo", s.handleUndo)
	mux.HandleFunc("POST /api/import/preview", s.handleImportPreview)
	mux.HandleFunc("POST /api/import/commit", s.handleImportCommit)
	mux.HandleFunc("POST /api/semesters", s.handleCreateSemester)
	mux.HandleFunc("POST /api/semesters/current", s.handleSetCurrentSemester)
	mux.HandleFunc("POST /api/semesters/delete", s.handleDeleteSemester)
	mux.HandleFunc("POST /api/hanzi/{id}/action", s.handleHanziAction)
	mux.HandleFunc("POST /api/hanzi/{id}/delete", s.handleHanziDelete)
	mux.HandleFunc("POST /api/settings", s.handleSaveSettings)
	mux.HandleFunc("POST /api/pinyin/recompute", s.handleRecomputePinyin)

	// ---- 静态资源 ----
	mux.Handle("GET /static/", s.staticHandler())

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok"))
	})

	return s.withMiddleware(mux)
}

// staticHandler 提供静态资源。dev 模式读磁盘，生产读 embed。
func (s *Server) staticHandler() http.Handler {
	var fsys fs.FS
	if s.dev {
		fsys = os.DirFS(s.assetsDir)
	} else {
		sub, err := fs.Sub(embeddedStatic, "static")
		if err != nil {
			s.log.Error("加载内嵌静态资源失败", "err", err)
			return http.NotFoundHandler()
		}
		fsys = sub
	}

	fileServer := http.FileServerFS(fsys)
	return http.StripPrefix("/static/", cacheControl(fileServer))
}

// cacheControl 给静态资源加上缓存头。开发模式下禁用缓存以便即时看到改动。
func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// ---------------- 中间件 ----------------

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 所有 HTML 页面都不许缓存。
		//
		// 没有这个头，浏览器会按启发式规则自行缓存，用户点「设置」
		// 时可能看到保存前的旧值——明明存进去了却显示没变。
		// 这个程序每次请求本来就要查库，缓存也没有收益。
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			w.Header().Set("Cache-Control", "no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
		}

		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("请求处理 panic", "path", r.URL.Path, "panic", rec)
				http.Error(w, "服务器内部错误", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)

		s.log.Debug("请求",
			"method", r.Method, "path", r.URL.Path,
			"elapsed", time.Since(start).String())
	})
}

// ---------------- 共用助手 ----------------

// today 返回当前学习日。
func (s *Server) today() day.Day {
	return day.FromTime(time.Now(), s.st.Location(), s.st.Cutoff())
}

// currentChildID 返回当前操作的孩子。
//
// 单机家用场景不做登录，取第一个孩子；将来要多孩子切换时，
// 从 cookie 或查询参数取即可，这里的签名不用改。
func (s *Server) currentChildID() (int64, error) {
	children, err := s.st.ListChildren()
	if err != nil {
		return 0, err
	}
	if len(children) == 0 {
		return 0, errors.New("还没有孩子档案")
	}
	return children[0].ID, nil
}

// writeJSON 输出 JSON。
func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("输出 JSON 失败", "err", err)
	}
}

// writeError 输出错误 JSON。
func (s *Server) writeError(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]any{"error": msg})
}

// readJSON 解析请求体。
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	// 限制请求体大小，避免超大粘贴把内存打满。
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// pathID 取出路径里的数字 ID。
func (s *Server) pathID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	if raw == "" {
		return 0, errors.New("缺少 " + name)
	}
	return strconv.ParseInt(raw, 10, 64)
}

// randomToken 生成一个随机字符串，用作导入预览的凭据。
func randomToken() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// 极罕见：退化成时间戳，仍能工作（只是不够随机）。
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// utf8Valid 判断文本是否是合法的 UTF-8。
func utf8Valid(s string) bool { return utf8.ValidString(s) }

// renderPage 渲染并输出一个页面。
//
// 先渲染到缓冲区：模板出错时才能干净地返回 500，
// 而不是发出 200 之后又写半截 HTML。
func (s *Server) renderPage(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.renderer.tpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("渲染模板失败", "template", name, "err", err)
		http.Error(w, "页面渲染失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}
