package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"strings"
)

//go:embed all:templates
var embeddedTemplates embed.FS

//go:embed all:static
var embeddedStatic embed.FS

// renderer 负责模板加载与执行。
//
// 支持两种模式：生产环境用 embed（单二进制），开发环境读磁盘（改了模板刷新即见）。
type renderer struct {
	dev bool
	dir string
	tpl *template.Template
}

func newRenderer(dev bool, dir string) (*renderer, error) {
	r := &renderer{dev: dev, dir: dir}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *renderer) load() error {
	funcs := template.FuncMap{
		"reasonLabel": reasonLabel,
		"add":         func(a, b int) int { return a + b },
		"pct":         percent,
		"seq":         seqInts,
		"statusLabel": statusLabel,
		"resultLabel": resultLabel,
	}

	var (
		tpl *template.Template
		err error
	)
	if r.dev {
		glob := path.Join(r.dir, "*.html")
		tpl, err = template.New("").Funcs(funcs).ParseGlob(glob)
	} else {
		tpl, err = template.New("").Funcs(funcs).ParseFS(embeddedTemplates, "templates/*.html")
	}
	if err != nil {
		return fmt.Errorf("加载模板: %w", err)
	}
	r.tpl = tpl
	return nil
}

// staticFS 返回静态资源的文件系统。
func staticFS(dev bool, dir string) (fs.FS, error) {
	if dev {
		return os.DirFS(dir), nil
	}
	return fs.Sub(embeddedStatic, "static")
}

func percent(part, total int) int {
	if total <= 0 {
		return 0
	}
	return part * 100 / total
}

func seqInts(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func reasonLabel(reason string) string {
	switch reason {
	case "new":
		return "新字"
	case "anchor1":
		return "第 2 天复习"
	case "anchor2":
		return "第 3 天复习"
	case "anchor6":
		return "第 7 天复习"
	case "sweep":
		return "滚动复习"
	case "manual":
		return "手动加入"
	default:
		return "该复习了"
	}
}

func statusLabel(status string) string {
	switch status {
	case "new":
		return "未学"
	case "learning":
		return "学习中"
	case "reviewing":
		return "复习中"
	case "mastered":
		return "已掌握"
	case "suspended":
		return "已暂停"
	default:
		return status
	}
}

func resultLabel(result string) string {
	if result == "known" {
		return "认识"
	}
	return "不认识"
}

// trimSpace 供模板里清理多行文本用。
func trimSpace(s string) string { return strings.TrimSpace(s) }
