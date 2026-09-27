package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/aurora/aurora/internal/buildinfo"
)

//go:embed dist
var panelFS embed.FS

// staticHandler serves the embedded single-page panel.
//
// The panel is fully self-contained: no CDN, no fonts, no analytics. It works
// on a phone with the radio off, which is the whole point of running a userbot
// from Termux.
func staticHandler() http.Handler {
	sub, err := fs.Sub(panelFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The shell must never be cached, or a rebuilt panel looks broken.
		w.Header().Set("Cache-Control", "no-store")

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(sub, name); err != nil {
			// Single-page app: unknown paths render the shell.
			name = "index.html"
		}
		if name == "index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			serveIndex(w, sub)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// serveIndex renders the panel shell with build metadata injected.
//
// The API token is deliberately NOT injected: it lives in an HttpOnly cookie
// set at first visit, so the page source is safe to cache and to screenshot.
func serveIndex(w http.ResponseWriter, sub fs.FS) {
	body, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "panel is not built", http.StatusInternalServerError)
		return
	}
	out := string(body)
	out = strings.ReplaceAll(out, "{{VERSION}}", buildinfo.Version)
	out = strings.ReplaceAll(out, "{{BUILT}}", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	_, _ = w.Write([]byte(out))
}

// serveGate is the anonymous sign-in shell. It contains nothing but a form:
// no token, no version, no endpoints.
func serveGate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(gateHTML))
}

const gateHTML = `<!DOCTYPE html>
<html lang="uk"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>Aurora — доступ</title>
<style>
:root{color-scheme:dark}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0b0f17;
 color:#e6edf7;font:15px/1.5 -apple-system,"Segoe UI",Roboto,sans-serif;
 background-image:radial-gradient(900px 400px at 15% -10%,rgba(110,168,254,.14),transparent 60%),
 radial-gradient(700px 380px at 95% 0%,rgba(167,139,250,.12),transparent 60%)}
.card{width:min(380px,calc(100% - 32px));background:#151d2e;border:1px solid #243049;
 border-radius:14px;padding:24px;text-align:center;box-shadow:0 8px 30px rgba(0,0,0,.35)}
h1{margin:0 0 6px;font-size:26px}
p{color:#8494b0;margin:0 0 16px}
input{width:100%;padding:12px 14px;margin-bottom:12px;background:#121826;color:#e6edf7;
 border:1px solid #243049;border-radius:10px;font:inherit;text-align:center;letter-spacing:2px;outline:none}
input:focus{border-color:#6ea8fe}
button{width:100%;padding:12px;border:none;border-radius:10px;font:inherit;font-weight:700;
 cursor:pointer;color:#0b0f17;background:linear-gradient(135deg,#6ea8fe,#a78bfa)}
.hint{font-size:12px;margin-top:14px;line-height:1.6}
code{background:#121826;padding:2px 6px;border-radius:6px;color:#6ea8fe;font-size:12px}
</style></head>
<body><div class="card">
<h1>🌌 Aurora</h1>
<p>Введіть токен, надрукований ядром у терміналі.</p>
<form onsubmit="return go(event)">
<input id="t" type="password" placeholder="токен" autocomplete="off" autofocus>
<button type="submit">Увійти</button>
</form>
<p class="hint">
Токен зберігається лише у HttpOnly-cookie цього браузера.<br>
Або відкрийте посилання виду <code>http://127.0.0.1:8420/?token=…</code>
</p>
</div>
<script>
function go(e){
  e.preventDefault();
  var t=document.getElementById('t').value.trim();
  if(!t) return false;
  location.href='/?token='+encodeURIComponent(t);
  return false;
}
</script>
</body></html>`
