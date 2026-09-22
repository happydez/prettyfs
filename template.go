package pretty

import (
	"fmt"
	"html/template"
)

const (
	pageOpen  = `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path class="bg" d="M6 2.75h8.25L19 7.5v13.75H6z"/><path d="M14.25 2.75V7.5H19"/>`
	pageClose = `</svg>`
)

var icons = map[string]template.HTML{
	"folder":   `<svg class="ic ic-folder" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M2.75 5.5c0-.97.78-1.75 1.75-1.75h4.6l2.1 2.4h8.3c.97 0 1.75.78 1.75 1.75v10.35c0 .97-.78 1.75-1.75 1.75H4.5c-.97 0-1.75-.78-1.75-1.75z"/><path class="cut" d="M2.75 9.4h18.5" stroke-width="1.3"/><path class="cut" d="M16.5 15.9h2.25" stroke-width="1.5" stroke-linecap="round"/></svg>`,
	"up":       `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 13.5 4.5 9 9 4.5"/><path d="M4.5 9h10a5 5 0 0 1 0 10H11"/></svg>`,
	"download": `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="square" aria-hidden="true"><path d="M12 3.5v12"/><path d="m6.5 10 5.5 5.5 5.5-5.5"/><path d="M4.5 20.5h15"/></svg>`,
	"doc":      pageOpen + `<path d="M8.75 11h7.5M8.75 14h7.5M8.75 17h4.5"/>` + pageClose,
	"code":     pageOpen + `<path d="m10.25 11.25-2 2.75 2 2.75M14.75 11.25l2 2.75-2 2.75M13 10.75l-1 6.5"/>` + pageClose,
	"image":    pageOpen + `<circle cx="10.25" cy="11.25" r="1.25" fill="currentColor" stroke="none"/><path d="m8 18.25 3.25-3.75 2.25 2.25 1.5-1.5L17 17.5"/>` + pageClose,
	"video":    pageOpen + `<path d="M10.5 10.75v6.5l5.25-3.25z" fill="currentColor"/>` + pageClose,
	"audio":    pageOpen + `<path d="M8.75 13.5v2M10.75 11.5v6M12.75 12.75v3.5M14.75 10.75v7.5M16.75 13v2.5"/>` + pageClose,
	"archive":  pageOpen + `<path d="M11 4.5h1.5M12.5 6.25H14M11 8h1.5M12.5 9.75H14"/><rect x="10.5" y="12" width="4" height="5" rx=".5" fill="currentColor" stroke="none"/>` + pageClose,
	"file":     pageOpen + `<circle cx="12.5" cy="14.5" r="1" fill="currentColor" stroke="none"/>` + pageClose,
}

func icon(name string) template.HTML {
	if s, ok := icons[name]; ok {
		return s
	}
	return icons["file"]
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

var funcs = template.FuncMap{"icon": icon, "bytes": formatBytes}

const baseCSS = `
:root{
  --paper:#ffffff;--ink:#0b0b0b;--muted:#6b6b6b;--faint:#a3a3a3;--line:#e6e6e6;--hover:#f5f5f5;
  --mono:ui-monospace,"JetBrains Mono","SF Mono","Cascadia Mono",Menlo,Consolas,monospace;
  --sans:"Helvetica Neue",Helvetica,Arial,system-ui,sans-serif;
  color-scheme:light;
}
*{box-sizing:border-box}
html,body{margin:0}
body{background:var(--paper);color:var(--ink);font:15px/1.5 var(--sans);-webkit-font-smoothing:antialiased}
a{color:inherit}
.wrap{max-width:1080px;margin:0 auto;padding:56px 32px 80px}
::selection{background:var(--ink);color:var(--paper)}
`

var listTmpl = template.Must(template.New("list").Funcs(funcs).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Path}} - {{.Title}}</title>
{{if .Favicon}}<link rel="icon" href="{{.Favicon}}">{{end}}
<style>` + baseCSS + `
header{padding-bottom:22px}
.title{margin:0 0 14px;font-size:13px;color:var(--muted);letter-spacing:.01em}
.path{margin:0;font:500 clamp(26px,4vw,40px)/1.1 var(--mono);letter-spacing:-.03em;word-break:break-all}
.path a{text-decoration:none;color:var(--faint);transition:color .12s}
.path a:hover{color:var(--ink)}
.path .cur{color:var(--ink)}
.list{border-top:2px solid var(--ink);border-bottom:2px solid var(--ink)}
.row{position:relative;display:grid;grid-template-columns:minmax(0,1fr) 170px 100px 44px;align-items:center;gap:16px;padding:11px 8px;border-top:1px solid var(--line)}
.row.head{border-top:0;padding:10px 8px;font-size:12px;color:var(--muted)}
.row.head a{text-decoration:none}.row.head a:hover{color:var(--ink)}
.row.head .on{color:var(--ink)}
.sort{position:relative}
.tri{position:absolute;top:50%;left:100%;margin:-3px 0 0 6px;width:8px;height:6px}
.row:not(.head):hover{background:var(--hover)}
.row:not(.head)::before{content:"";position:absolute;left:0;top:-1px;bottom:0;width:2px;background:var(--ink);transform:scaleY(0);transition:transform .12s}
.row:not(.head):hover::before,.row:focus-within::before{transform:scaleY(1)}
.name{display:flex;align-items:center;gap:14px;min-width:0}
.name a{text-decoration:none;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.name a::after{content:"";position:absolute;inset:0}
.name a:focus-visible{outline:none}
.row.dir .name a{font-weight:600}
.ic{width:24px;height:24px;flex:none;color:var(--ink)}
.ic .bg{fill:var(--paper)}.ic .cut{stroke:var(--paper);fill:none}
.row:hover .ic .bg{fill:var(--hover)}
.ext{flex:none;font:500 10px/1 var(--mono);color:var(--muted);border:1px solid var(--line);padding:3px 5px;letter-spacing:.02em}
.date,.size{font:13px/1.2 var(--mono);color:var(--muted);white-space:nowrap;font-variant-numeric:tabular-nums}
.size{text-align:center}
.row.head .date,.row.head .size{font:inherit;color:inherit}
.dl{position:relative;z-index:1;display:grid;place-items:center;width:32px;height:32px;border:1px solid transparent;color:var(--ink)}
.row:hover .dl{border-color:var(--ink)}
.dl:hover,.dl:focus-visible{background:var(--ink);color:var(--paper);outline:none}
.empty{padding:56px 8px;text-align:center;color:var(--muted);border-top:1px solid var(--line);font-family:var(--mono);font-size:13px}
.stats{display:flex;flex-wrap:wrap;gap:8px 36px;padding:16px 8px 0;font:12px/1.2 var(--mono);color:var(--muted)}
.stats b{color:var(--ink);font-weight:600;margin-left:6px}
@media (max-width:680px){
  .wrap{padding:32px 18px 56px}
  .row{grid-template-columns:minmax(0,1fr) 78px 36px;gap:10px}
  .date{display:none}
}
</style>
</head>
<body>
<div class="wrap">
<header>
  <div>
    <p class="title">{{.Title}}</p>
    <h1 class="path">{{range .Crumbs}}{{if .Last}}<span class="cur">{{.Name}}</span>{{else}}<a href="{{.Href}}">{{.Name}}</a>{{end}}{{end}}</h1>
  </div>
</header>
<main class="list">
  <div class="row head">
    <div>{{with index .Sort "name"}}<a class="sort{{if .Dir}} on{{end}}" href="{{.Href}}">Name{{template "tri" .Dir}}</a>{{end}}</div>
    <div class="date">{{with index .Sort "date"}}<a class="sort{{if .Dir}} on{{end}}" href="{{.Href}}">Modified{{template "tri" .Dir}}</a>{{end}}</div>
    <div class="size">{{with index .Sort "size"}}<a class="sort{{if .Dir}} on{{end}}" href="{{.Href}}">Size{{template "tri" .Dir}}</a>{{end}}</div>
    <span></span>
  </div>
  {{if .HasParent}}
  <div class="row">
    <div class="name">{{icon "up"}}<a href="../">..</a></div>
    <div class="date"></div><div class="size"></div><div></div>
  </div>
  {{end}}
  {{range .Entries}}
  <div class="row{{if .IsDir}} dir{{end}}">
    <div class="name">{{icon .Kind}}<a href="{{.Href}}" title="{{.Name}}">{{.Name}}{{if .IsDir}}/{{end}}</a>{{if .Ext}}<span class="ext">{{.Ext}}</span>{{end}}</div>
    <div class="date" title="{{.ModTime.Format "2006-01-02 15:04:05 MST"}}">{{.ModTime.Format "2006-01-02 15:04"}}</div>
    <div class="size">{{if .IsDir}}—{{else}}{{bytes .Size}}{{end}}</div>
    {{if or (not .IsDir) $.Zip}}<a class="dl" href="{{.DownloadHref}}" title="Download{{if .IsDir}} as .zip{{end}}" aria-label="Download {{.Name}}">{{icon "download"}}</a>{{else}}<div></div>{{end}}
  </div>
  {{end}}
  {{if not .Entries}}<div class="empty">empty folder</div>{{end}}
  {{if .Truncated}}<div class="empty">showing the first {{.Shown}} entries</div>{{end}}
</main>
<footer class="stats">
  <span>folders<b>{{.TotalDirs}}</b></span>
  <span>files<b>{{.TotalFiles}}</b></span>
  <span>size<b>{{bytes .TotalSize}}</b></span>
</footer>
</div>
</body>
</html>
{{define "tri"}}{{if eq . "asc"}}<svg class="tri" viewBox="0 0 8 6" aria-label="ascending"><path d="M4 0l4 6H0z" fill="currentColor"/></svg>{{else if eq . "desc"}}<svg class="tri" viewBox="0 0 8 6" aria-label="descending"><path d="M0 0h8L4 6z" fill="currentColor"/></svg>{{end}}{{end}}`))

var errorTmpl = template.Must(template.New("error").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Code}} {{.Status}} - {{.Title}}</title>
{{if .Favicon}}<link rel="icon" href="{{.Favicon}}">{{end}}
<style>` + baseCSS + `
.box{max-width:560px;padding-top:10vh}
.code{margin:0;font:500 clamp(72px,14vw,128px)/.9 var(--mono);letter-spacing:-.06em;color:var(--ink)}
h1{margin:28px 0 0;padding-top:18px;border-top:2px solid var(--ink);font-size:22px;font-weight:600}
.actions{display:flex;gap:12px;margin-top:32px}
.btn{display:inline-block;padding:10px 16px;border:1px solid var(--ink);font:13px var(--mono);text-decoration:none;cursor:pointer;background:none;color:var(--ink)}
.btn:hover,.btn.primary{background:var(--ink);color:var(--paper)}
.btn.primary:hover{background:#333}
</style>
</head>
<body><div class="wrap"><div class="box">
<p class="code">{{.Code}}</p>
<h1>{{.Status}}</h1>
<div class="actions">
  <button class="btn" type="button" id="back">back</button>
  {{if .RetryAfter}}<button class="btn primary" type="button" id="retry">try again</button>{{end}}
</div>
</div></div>
<script>
document.getElementById('back').addEventListener('click',function(){
  if(history.length>1){history.back();}else{location.href='./';}
});
</script>
{{if .RetryAfter}}<script>
document.getElementById('retry').addEventListener('click',function(){location.reload();});
</script>{{end}}
</body>
</html>`))
