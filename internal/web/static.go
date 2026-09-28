package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
)

//go:embed dist
var panelFS embed.FS

// staticHandler serves the embedded single-page panel.
// The token is deliberately NOT embedded into the HTML: inline scripts are
// blocked by our own CSP (script-src 'self') and any token in the markup is
// readable by any injected JS. Auth relies on ?token= on first visit (the SPA
// stores it) plus the HttpOnly cookie the middleware sets.
func staticHandler(token string) http.Handler {
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
			serveIndex(w, sub, token)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// serveIndex renders the panel shell with build metadata.
// No secret is ever written into the markup (see staticHandler).
func serveIndex(w http.ResponseWriter, sub fs.FS, token string) {
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

// serveGate renders the sign-in shell. It auto-redirects with token so users never have to type it.
func serveGate(w http.ResponseWriter, r *http.Request, token string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if token != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "aurora_token",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   31536000,
		})
	}
	w.WriteHeader(http.StatusOK)
	body := strings.ReplaceAll(gateHTML, "{{TOKEN}}", token)
	_, _ = w.Write([]byte(body))
}

const gateHTML = `<!DOCTYPE html>
<html lang="uk"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="dark">
<meta name="theme-color" content="#08090c">
<title>Aurora — Авторизація</title>
<style>
:root{color-scheme:dark}
*{box-sizing:border-box;margin:0;padding:0}
body{
  min-height:100vh;display:grid;place-items:center;
  background:#08090c;color:#f8fafc;
  font:14px/1.5 -apple-system,BlinkMacSystemFont,"Inter","Segoe UI",Roboto,sans-serif;
  letter-spacing:-0.01em;padding:20px;
}
.card{
  width:min(360px,100%);background:#0e1017;border:1px solid rgba(255,255,255,0.1);
  border-radius:14px;padding:28px 24px;text-align:center;
  box-shadow:0 12px 36px -4px rgba(0,0,0,0.7);
}
.brand-icon{
  width:40px;height:40px;margin:0 auto 14px;
  background:#141722;border:1px solid rgba(255,255,255,0.12);
  border-radius:10px;display:flex;align-items:center;justify-content:center;
  color:#818cf8;box-shadow:0 0 20px rgba(99,102,241,0.2);
}
h1{margin:0 0 6px;font-size:20px;font-weight:700;color:#f8fafc;letter-spacing:-0.02em}
p{color:#94a3b8;font-size:13px;margin:0 0 20px}
input{
  width:100%;padding:11px 14px;margin-bottom:12px;background:#10131d;color:#f8fafc;
  border:1px solid rgba(255,255,255,0.12);border-radius:8px;font:inherit;font-size:14px;
  text-align:center;letter-spacing:1px;outline:none;transition:border-color .15s,box-shadow .15s;
}
input:focus{border-color:#6366f1;box-shadow:0 0 0 3px rgba(99,102,241,0.25)}
button{
  width:100%;padding:11px;border:none;border-radius:8px;font:inherit;font-size:13px;
  font-weight:600;cursor:pointer;color:#ffffff;background:#6366f1;
  transition:background .15s;box-shadow:0 1px 8px rgba(99,102,241,0.25);
}
button:hover{background:#4f46e5}
.hint{font-size:11px;color:#64748b;margin-top:18px;line-height:1.6}
code{background:#10131d;padding:2px 6px;border-radius:4px;color:#818cf8;font-size:11px;font-family:monospace}
</style></head>
<body><div class="card">
<div class="brand-icon">
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"></polygon>
  </svg>
</div>
<h1>Aurora</h1>
<p id="msg">Автоматичний вхід у веб-панель...</p>
<form onsubmit="return go(event)">
<input id="t" type="password" placeholder="токен доступу" value="{{TOKEN}}" autocomplete="off" autofocus>
<button type="submit">Увійти</button>
</form>
<p class="hint">
Токен підставляється автоматично.<br>
Або використовуйте посилання: <code>http://127.0.0.1:8420/?token=…</code>
</p>
</div>
<script>
(function() {
  var t = "{{TOKEN}}" || localStorage.getItem("aurora_token") || "";
  if (t) {
    try {
      localStorage.setItem("aurora_token", t);
      document.cookie = "aurora_token=" + encodeURIComponent(t) + "; path=/; max-age=31536000; SameSite=Lax";
    } catch(e) {}
    var el = document.getElementById('t');
    if (el) el.value = t;
    location.replace("/?token=" + encodeURIComponent(t));
  }
})();
function go(e){
  e.preventDefault();
  var t = (document.getElementById('t').value || "{{TOKEN}}").trim();
  if(!t) return false;
  try {
    localStorage.setItem("aurora_token", t);
    document.cookie = "aurora_token=" + encodeURIComponent(t) + "; path=/; max-age=31536000; SameSite=Lax";
  } catch(e) {}
  location.replace('/?token=' + encodeURIComponent(t));
  return false;
}
</script>
</body></html>`
