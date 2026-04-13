package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

const landingPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sports Stream API</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: 'Segoe UI', Arial, sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 40px 20px; }
  .badge { display: inline-flex; align-items: center; gap: 8px; background: #134e4a; border: 1px solid #14b8a6; border-radius: 100px; padding: 6px 16px; font-size: 12px; color: #5eead4; letter-spacing: 0.5px; margin-bottom: 32px; }
  .dot { width: 7px; height: 7px; background: #10b981; border-radius: 50%; animation: pulse 2s infinite; }
  @keyframes pulse { 0%,100%{opacity:1;transform:scale(1)} 50%{opacity:0.6;transform:scale(1.3)} }
  h1 { font-size: 42px; font-weight: 700; color: #f8fafc; margin-bottom: 8px; letter-spacing: -0.5px; }
  .subtitle { font-size: 16px; color: #64748b; margin-bottom: 48px; }
  .grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 12px; width: 100%; max-width: 680px; margin-bottom: 40px; }
  .card { background: #1e293b; border: 1px solid #334155; border-radius: 12px; padding: 20px; }
  .card-header { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
  .dot-color { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
  .card-title { font-size: 13px; font-weight: 600; color: #f1f5f9; }
  .card-port { font-size: 11px; color: #475569; font-family: monospace; }
  .endpoint { font-size: 11px; color: #64748b; margin: 3px 0; font-family: monospace; }
  .endpoint span { color: #38bdf8; }
  .divider { width: 100%; max-width: 680px; border: none; border-top: 1px solid #1e293b; margin: 8px 0 32px; }
  .links { display: flex; gap: 12px; flex-wrap: wrap; justify-content: center; }
  .btn { display: inline-flex; align-items: center; gap: 6px; padding: 10px 20px; border-radius: 8px; font-size: 13px; font-weight: 500; text-decoration: none; cursor: pointer; border: none; transition: opacity 0.15s; }
  .btn:hover { opacity: 0.85; }
  .btn-primary { background: #0ea5e9; color: #fff; }
  .btn-outline { background: transparent; color: #94a3b8; border: 1px solid #334155; }
  .footer { margin-top: 48px; font-size: 11px; color: #334155; }
</style>
</head>
<body>
<div class="badge"><span class="dot"></span>All systems operational</div>
<h1>Sports Stream API</h1>
<p class="subtitle">Cloud-Native Live Sports Streaming Platform &middot; CCC&apos;26</p>
<div class="grid">
  <div class="card">
    <div class="card-header"><div class="dot-color" style="background:#8b5cf6"></div><div><div class="card-title">user-service</div><div class="card-port">:8081</div></div></div>
    <div class="endpoint"><span>POST</span> /api/v1/auth/verify</div>
    <div class="endpoint"><span>GET</span>  /api/v1/users/me</div>
    <div class="endpoint"><span>PATCH</span> /api/v1/users/me</div>
  </div>
  <div class="card">
    <div class="card-header"><div class="dot-color" style="background:#f97316"></div><div><div class="card-title">stream-service</div><div class="card-port">:8082</div></div></div>
    <div class="endpoint"><span>GET</span>  /api/v1/streams</div>
    <div class="endpoint"><span>POST</span> /api/v1/streams</div>
    <div class="endpoint"><span>POST</span> /api/v1/streams/:id/join</div>
  </div>
  <div class="card">
    <div class="card-header"><div class="dot-color" style="background:#ef4444"></div><div><div class="card-title">notification-service</div><div class="card-port">:8083</div></div></div>
    <div class="endpoint"><span>POST</span> /api/v1/notifications/test</div>
    <div class="endpoint"><span>GET</span>  /health/notification</div>
  </div>
  <div class="card">
    <div class="card-header"><div class="dot-color" style="background:#10b981"></div><div><div class="card-title">analytics-service</div><div class="card-port">:8085</div></div></div>
    <div class="endpoint"><span>GET</span>  /api/v1/analytics/stream/:id</div>
    <div class="endpoint"><span>GET</span>  /health/analytics</div>
  </div>
</div>
<hr class="divider">
<div class="links">
  <a class="btn btn-primary" href="/health">Health Check</a>
  <a class="btn btn-outline" href="/api/v1/streams">Live Streams</a>
</div>
<p class="footer">Sports Stream Platform &middot; CCC&apos;26 Cloud Computing Competition &middot; March 2026</p>
</body>
</html>`

func newProxy(target string) *httputil.ReverseProxy {
	u, _ := url.Parse(target)
	return httputil.NewSingleHostReverseProxy(u)
}

func main() {
	// Scalingo injects PORT — must listen on it or app times out
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("GATEWAY_PORT")
	}
	if port == "" {
		port = "8080"
	}

	//// NEW — read from environment (works on Cloud Run)
	//userProxy := newProxy(util.MustGetenv("USER_SERVICE_URL"))
	//streamProxy := newProxy(util.MustGetenv("STREAM_SERVICE_URL"))
	//analyticsProxy := newProxy(util.MustGetenv("ANALYTICS_SERVICE_URL"))
	//notificationProxy := newProxy(util.MustGetenv("NOTIFICATION_SERVICE_URL"))

	userProxy := newProxy("http://127.0.0.1:8081")
	streamProxy := newProxy("http://127.0.0.1:8082")
	notificationProxy := newProxy("http://127.0.0.1:8083")
	analyticsProxy := newProxy("http://127.0.0.1:8085")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		log.Printf(`{"gateway":"route","method":%q,"path":%q}`, r.Method, path)

		switch {

		// ── Root → landing page ────────────────────────────────────────────
		case path == "/" || path == "":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(landingPage))

		// ── Health checks ──────────────────────────────────────────────────
		case path == "/health":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"gateway":"ok","services":{"user":"http://127.0.0.1:8081","stream":"http://127.0.0.1:8082","notification":"http://127.0.0.1:8083","analytics":"http://127.0.0.1:8085"}}`))

		case path == "/health/user":
			r.URL.Path = "/health"
			userProxy.ServeHTTP(w, r)

		case path == "/health/stream":
			r.URL.Path = "/health"
			streamProxy.ServeHTTP(w, r)

		case path == "/health/notification":
			r.URL.Path = "/health"
			notificationProxy.ServeHTTP(w, r)

		case path == "/health/analytics":
			r.URL.Path = "/health"
			analyticsProxy.ServeHTTP(w, r)

		// ── notification-service ───────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/notifications"):
			notificationProxy.ServeHTTP(w, r)

		// ── analytics-service ──────────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/analytics"):
			analyticsProxy.ServeHTTP(w, r)

		// ── stream-service ─────────────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/streams"):
			streamProxy.ServeHTTP(w, r)

		// ── user-service ───────────────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/auth"):
			userProxy.ServeHTTP(w, r)

		case strings.HasPrefix(path, "/api/v1/users"):
			userProxy.ServeHTTP(w, r)

		// ── default → user-service ─────────────────────────────────────────
		default:
			userProxy.ServeHTTP(w, r)
		}
	})

	log.Printf(`{"gateway":"started","port":%q}`, port)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("gateway: %v", err)
	}
}
