// Package web 是内嵌的网页（原生 JS 单页应用，没有构建步骤）。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var static embed.FS

// rootIcons 是浏览器与系统不看页面、直接到站点根上找的图标（不认 SVG 的 Safari、书签、iOS 加到主屏幕）。
// 位图由 scripts/render_icons.py 从 favicon.svg 生成。
var rootIcons = map[string]struct{ file, contentType string }{
	"/favicon.ico":                      {"favicon.ico", "image/x-icon"},
	"/apple-touch-icon.png":             {"apple-touch-icon.png", "image/png"},
	"/apple-touch-icon-precomposed.png": {"apple-touch-icon.png", "image/png"},
}

// Handler 提供 /assets/ 下的静态文件与站点根上的图标；其余路径一律回 index.html（单页应用自己处理路由）。
func Handler() http.Handler {
	root, _ := fs.Sub(static, "static")
	files := http.StripPrefix("/assets/", http.FileServer(http.FS(root)))
	index, _ := fs.ReadFile(root, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if icon, ok := rootIcons[r.URL.Path]; ok {
			data, err := fs.ReadFile(root, icon.file)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", icon.contentType)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(data)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
