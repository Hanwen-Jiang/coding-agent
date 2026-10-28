// Package web 是内嵌的网页（原生 JS 单页应用，没有构建步骤）。
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
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
	return newHandler(root)
}

// assets 是一版网页：文件内容（引用已改写）与这一版的版本号。
type assets struct {
	version string
	files   map[string][]byte
}

// load 读出全部静态文件，算版本号（全部文件名与内容的哈希），并把页面里对 /assets/ 的引用、JS 模块之间的 import
// 都加上 ?v=<版本>。Cloudflare 与浏览器按 URL 缓存 /assets/（Cloudflare 还会把 no-cache 改成 4 小时），换了版本 URL 就变，
// 不会拿旧的 JS、CSS 配新的页面。任何一个文件变了所有引用一起变，比逐个文件算哈希简单，文件也不大。
func load(root fs.FS) assets {
	files := map[string][]byte{}
	_ = fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, err := fs.ReadFile(root, name); err == nil {
				files[name] = data
			}
		}
		return nil
	})
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(files[name])
		h.Write([]byte{0})
	}
	version := hex.EncodeToString(h.Sum(nil))[:12]
	for _, name := range names {
		switch path.Ext(name) {
		case ".html":
			for _, ref := range names {
				files[name] = bytes.ReplaceAll(files[name], []byte(`"/assets/`+ref+`"`), []byte(`"/assets/`+ref+`?v=`+version+`"`))
			}
		case ".js":
			for _, ref := range names {
				if path.Ext(ref) == ".js" {
					files[name] = bytes.ReplaceAll(files[name], []byte(`"./`+ref+`"`), []byte(`"./`+ref+`?v=`+version+`"`))
				}
			}
		}
	}
	return assets{version: version, files: files}
}

func newHandler(root fs.FS) http.Handler {
	a := load(root)
	index := a.files["index.html"]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if icon, ok := rootIcons[r.URL.Path]; ok {
			data, found := a.files[icon.file]
			if !found {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", icon.contentType)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(data)
			return
		}
		if name, ok := strings.CutPrefix(r.URL.Path, "/assets/"); ok {
			data, found := a.files[name]
			if !found || name == "index.html" {
				http.NotFound(w, r)
				return
			}
			// 带着这一版的版本号：内容永远不变，可以长期缓存；不带或是别的版本（旧页面来要）：每次都问。
			if r.URL.Query().Get("v") == a.version {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
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
