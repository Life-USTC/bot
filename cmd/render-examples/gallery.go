package main

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
)

type example struct {
	Name   string
	Title  string
	File   string
	Width  int
	Height int
}

func writeGallery(dir string, examples []example) error {
	var output bytes.Buffer
	if err := galleryTemplate.Execute(&output, examples); err != nil {
		return fmt.Errorf("build example gallery: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), output.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write example gallery: %w", err)
	}
	return nil
}

var galleryTemplate = template.Must(template.New("gallery").Parse(`<!doctype html>
<html lang="zh-CN">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bot · Typst 手机图卡</title>
<style>
* { box-sizing: border-box; }
body { margin: 0; padding: 24px; background: #e9eaee; color: #27272a; font: 15px/1.5 system-ui, sans-serif; }
header { max-width: 1218px; margin: 0 auto 24px; }
h1 { font-size: 24px; margin: 0 0 8px; }
p { margin: 0; color: #52525b; }
main { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 390px), 390px)); gap: 24px; justify-content: center; align-items: start; }
figure { width: 100%; margin: 0; background: #fafafa; }
figcaption { padding: 12px 20px; border-bottom: 1px solid #d4d4d8; }
figcaption span { display: block; font-size: 13px; color: #52525b; }
a { color: inherit; }
img { display: block; width: 100%; height: auto; }
@media (max-width: 438px) { body { padding: 0; } header { padding: 20px; } }
</style>
<header>
<h1>Bot · Typst 手机图卡</h1>
<p>固定示例数据 · 390pt 画布 · 3× 清晰度 · 正文 17pt / 注释 13pt。点击图片可查看原始 PNG。</p>
</header>
<main>
{{range .}}<figure id="{{.Name}}">
<figcaption>{{.Title}}<span>{{.Width}} × {{.Height}} px</span></figcaption>
<a href="{{.File}}"><img src="{{.File}}" alt="{{.Title}}" width="{{.Width}}" height="{{.Height}}"></a>
</figure>{{end}}
</main>
</html>`))
