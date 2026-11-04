// Package oauthpage renders the page a person sees after a provider redirects
// back from sign-in. One self-contained template (inline CSS, no external
// assets) serves every provider callback so the result reads the same way.
package oauthpage

import (
	"html/template"
	"net/http"
	"strings"
)

// Page describes one sign-in result. Detail and Notice are plain text.
type Page struct {
	// Provider is an engine or catalogue provider ID (codex, claude, anthropic,
	// antigravity, xai, kimi, devin, meta, gemini); unknown IDs render neutral.
	Provider string
	Success  bool
	// Detail is the provider's failure reason, shown on failure pages.
	Detail string
	// Notice is an optional extra line, linked to NoticeURL when that is http(s).
	Notice    string
	NoticeURL string
	// DashboardURL links back to the pool; empty omits the link (CLI-local servers).
	DashboardURL string
	// AutoClose closes the tab after a few seconds when the browser allows it.
	AutoClose bool
}

// DashboardPath is the pool page on the NeoProxy origin.
const DashboardPath = "/neo/#/"

type brand struct {
	name    string
	mark    string
	hue     int
	neutral bool
}

var brands = map[string]brand{
	"codex":       {"ChatGPT", "C", 165, false},
	"openai":      {"ChatGPT", "C", 165, false},
	"claude":      {"Claude", "C", 45, false},
	"anthropic":   {"Claude", "C", 45, false},
	"antigravity": {"Antigravity", "A", 255, false},
	"gemini":      {"Gemini", "G", 255, false},
	"xai":         {"Grok", "G", 0, true},
	"kimi":        {"Kimi", "K", 340, false},
	"devin":       {"Devin", "D", 205, false},
	"meta":        {"Meta AI", "M", 280, false},
}

type view struct {
	Title        string
	Heading      string
	Lead         string
	Detail       string
	Notice       string
	NoticeURL    string
	DashboardURL string
	Mark         string
	MarkColor    template.CSS
	MarkInk      template.CSS
	Success      bool
	AutoClose    bool
}

func safeHTTPURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		return raw
	}
	return ""
}

func viewFor(p Page) view {
	b, ok := brands[strings.ToLower(strings.TrimSpace(p.Provider))]
	if !ok {
		name := strings.TrimSpace(p.Provider)
		if name == "" {
			name = "the provider"
		}
		b = brand{name: name, mark: strings.ToUpper(firstRune(name)), neutral: true}
	}
	chroma := "0.13"
	if b.neutral {
		chroma = "0.012"
	}
	v := view{
		Mark:         b.mark,
		MarkColor:    template.CSS("oklch(0.74 " + chroma + " " + itoa(b.hue) + ")"),
		MarkInk:      template.CSS("oklch(0.22 0.03 " + itoa(b.hue) + ")"),
		Success:      p.Success,
		AutoClose:    p.AutoClose && p.Success,
		Notice:       strings.TrimSpace(p.Notice),
		NoticeURL:    safeHTTPURL(p.NoticeURL),
		DashboardURL: strings.TrimSpace(p.DashboardURL),
	}
	if p.Success {
		v.Title = "Signed in to " + b.name
		v.Heading = "Signed in to " + b.name
		v.Lead = "You can close this tab. NeoProxy added this account to the pool."
	} else {
		v.Title = "Sign-in to " + b.name + " did not finish"
		v.Heading = "Sign-in to " + b.name + " did not finish"
		v.Lead = "Nothing was added to the pool. Close this tab and start the sign-in again from the pool."
		v.Detail = strings.TrimSpace(p.Detail)
		if len(v.Detail) > 400 {
			v.Detail = v.Detail[:400]
		}
	}
	return v
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return "?"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

var page = template.Must(template.New("oauth").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style>
:root{--chassis:#0D0A13;--surface:#15111D;--raised:#1D1828;--line:#2B2437;--ink:#EDE9F5;--ink-2:#A79FB8;--ink-3:#6E6681;--accent:#B48CFF;--good:#5DD6A2;--warn:#F2B45A;--bad:#F27B7B;color-scheme:dark}
@media (prefers-color-scheme:light){:root{--chassis:#F6F4FA;--surface:#FFFFFF;--raised:#F0EDF6;--line:#E2DCEC;--ink:#1A1523;--ink-2:#5B536B;--ink-3:#8A829A;--accent:#7A4FE0;--good:#1E9E6A;--warn:#B7791F;--bad:#C94444;color-scheme:light}}
*{box-sizing:border-box}
html,body{margin:0;height:100%}
body{display:grid;place-items:center;padding:24px;background:var(--chassis);color:var(--ink);font:400 14px/1.5 "Mona Sans","Mona Sans Variable",ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;-webkit-font-smoothing:antialiased}
main{width:100%;max-width:440px;background:var(--surface);border:1px solid var(--line);border-radius:14px;padding:28px}
.top{display:flex;align-items:center;gap:14px;margin-bottom:20px}
.mark{flex:none;width:44px;height:44px;border-radius:10px;display:grid;place-items:center;background:{{.MarkColor}};color:{{.MarkInk}};font:700 20px/1 "Mona Sans",ui-sans-serif,system-ui,sans-serif;letter-spacing:-.02em}
.state{display:inline-flex;align-items:center;gap:6px;padding:2px 8px;border-radius:6px;font-size:12px;font-weight:600;background:var(--raised);border:1px solid var(--line)}
.state i{width:7px;height:7px;border-radius:50%;background:var(--good)}
.fail .state i{background:var(--bad)}
h1{margin:0 0 10px;font-size:20px;line-height:1.3;font-weight:700;font-stretch:112%;letter-spacing:-.01em}
p{margin:0 0 12px;color:var(--ink-2)}
.detail{margin:4px 0 14px;padding:10px 12px;border-radius:10px;background:var(--raised);border:1px solid var(--line);color:var(--ink);font:13px/1.45 "Monaspace Neon",ui-monospace,SFMono-Regular,Menlo,monospace;overflow-wrap:anywhere}
.notice{color:var(--warn)}
.foot{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-top:20px;padding-top:16px;border-top:1px solid var(--line)}
a{color:var(--accent);text-decoration:none;font-weight:600}
a:hover{text-decoration:underline}
a:focus-visible{outline:2px solid var(--accent);outline-offset:3px;border-radius:4px}
.brand{color:var(--ink-3);font-size:12px}
</style>
</head>
<body>
<main class="{{if .Success}}ok{{else}}fail{{end}}">
<div class="top"><div class="mark" aria-hidden="true">{{.Mark}}</div><span class="state"><i></i>{{if .Success}}Signed in{{else}}Not signed in{{end}}</span></div>
<h1>{{.Heading}}</h1>
<p>{{.Lead}}</p>
{{if .Detail}}<p>The provider reported:</p><div class="detail">{{.Detail}}</div>{{end}}
{{if .Notice}}<p class="notice">{{if .NoticeURL}}<a href="{{.NoticeURL}}" rel="noopener">{{.Notice}}</a>{{else}}{{.Notice}}{{end}}</p>{{end}}
{{if .AutoClose}}<p class="brand">This tab closes by itself in a few seconds.</p>{{end}}
<div class="foot">{{if .DashboardURL}}<a href="{{.DashboardURL}}">Open the pool in NeoProxy</a>{{else}}<span class="brand">Return to the terminal to continue.</span>{{end}}<span class="brand">NeoProxy</span></div>
</main>
{{if .AutoClose}}<script>setTimeout(function(){window.close();},5000);</script>{{end}}
</body>
</html>`))

// Render returns the complete HTML document for p.
func Render(p Page) string {
	var b strings.Builder
	if err := page.Execute(&b, viewFor(p)); err != nil {
		return "<!doctype html><title>NeoProxy</title><p>Sign-in finished. You can close this tab.</p>"
	}
	return b.String()
}

// Write sends the page with status, never cached.
func Write(w http.ResponseWriter, status int, p Page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(Render(p)))
}
