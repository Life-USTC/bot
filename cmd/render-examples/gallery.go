package main

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
)

type example struct {
	Name          string
	Title         string
	File          string
	Width         int
	Height        int
	ReferenceFile string
}

func writeGallery(dir string, examples []example) error {
	for i := range examples {
		file := filepath.Join("reference", examples[i].Name+".png")
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			examples[i].ReferenceFile = filepath.ToSlash(file)
		}
	}
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
<title>Bot · 渲染对照</title>
<style>
* { box-sizing: border-box; }
body { margin: 0; padding: 32px; background: #eee; color: #27272a; font: 14px/1.6 system-ui, sans-serif; }
header, main { max-width: 1500px; margin: auto; }
h1 { font-size: 22px; margin: 0 0 8px; }
p { margin: 0 0 32px; color: #71717a; }
section { margin-bottom: 48px; }
h2 { font-size: 16px; margin: 0 0 12px; }
.pair { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 480px), 1fr)); gap: 24px; align-items: start; }
figure { margin: 0; min-width: 0; }
figcaption { margin-bottom: 8px; color: #71717a; }
a { color: inherit; }
img { display: block; width: 100%; height: auto; background: #fafafa; }
@media (max-width: 640px) { body { padding: 16px; } }
</style>
<header>
<h1>Bot · 渲染对照</h1>
<p>课表使用 2026 秋季公开教学班组合，校车保留公开时刻表的全部班次；数据快照采集于 2026-09-09。课表固定展示第 8 周，工作日校车时钟为周三 15:04，周末为周六 15:04。天气和待办使用合成数据。点击图片查看完整 PNG。</p>
<p>来源：<a href="https://catalog.ustc.edu.cn/api/teach/lesson/list-for-teach/461">学校公开教学班</a> · <a href="https://www.teach.ustc.edu.cn/calendar/20135.html">教学日历与上课时间</a> · <a href="https://static.life-ustc.tiankaima.dev/bus_data_v3.json">校车生产数据源</a>。</p>
<p>左侧旧版图片仅作历史样式参考，使用不同的旧示例数据与时间，并非同数据对照。原图为 2×，Typst 为 3×。</p>
</header>
<main>
{{range .}}<section id="{{.Name}}">
<h2>{{.Title}}</h2>
<div class="pair">
{{if .ReferenceFile}}<figure><figcaption>历史样式参考 · 旧示例数据</figcaption><a href="{{.ReferenceFile}}"><img src="{{.ReferenceFile}}" alt="{{.Title}} · 原始 Go 渲染"></a></figure>{{end}}
<figure><figcaption>Typst · {{.Width}} × {{.Height}} px</figcaption><a href="{{.File}}"><img src="{{.File}}" alt="{{.Title}} · Typst" width="{{.Width}}" height="{{.Height}}"></a></figure>
</div>
</section>{{end}}
</main>
</html>`))
