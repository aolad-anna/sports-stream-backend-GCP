package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sports-stream-backend/pkg/util"
	"strings"
	"time"
)

type serviceHealth struct {
	Status  string `json:"status"`
	URL     string `json:"url"`
	Details string `json:"details,omitempty"`
}

func checkHealth(client *http.Client, endpoint string) serviceHealth {
	resp, err := client.Get(endpoint)
	if err != nil {
		return serviceHealth{Status: "down", URL: endpoint, Details: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return serviceHealth{Status: "up", URL: endpoint}
	}

	return serviceHealth{
		Status:  "down",
		URL:     endpoint,
		Details: resp.Status,
	}
}

func renderHealthPage(overall string, services map[string]serviceHealth) string {
	badgeClass := "warn"
	badgeText := "System status: degraded"
	if overall == "ok" {
		badgeClass = "up"
		badgeText = "System status: healthy"
	}

	order := []string{"user", "stream", "notification", "admin", "analytics"}
	var cards strings.Builder
	for _, name := range order {
		svc := services[name]
		className := "down"
		if svc.Status == "up" {
			className = "up"
		}
		cards.WriteString(fmt.Sprintf(
			"<div class=\"card\"><div class=\"service\">%s</div><div class=\"pill %s\">%s</div><div class=\"meta\">%s</div><div class=\"small\">%s</div></div>",
			html.EscapeString(strings.Title(name)+" service"),
			className,
			html.EscapeString(strings.ToUpper(svc.Status)),
			html.EscapeString(svc.URL),
			html.EscapeString(svc.Details),
		))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sports Stream Health</title>
<style>
*{box-sizing:border-box} body{margin:0;font-family:Segoe UI,Arial,sans-serif;background:#0f172a;color:#e2e8f0;padding:24px}
.shell{max-width:1100px;margin:0 auto}.panel{background:#111827;border:1px solid #334155;border-radius:20px;padding:24px}
.badge{display:inline-block;padding:8px 12px;border-radius:999px;font-size:12px;font-weight:700;margin-bottom:16px}.badge.up{background:#153d2f;color:#86efac}.badge.warn{background:#4a2d13;color:#fdba74}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:14px;margin-top:18px}.card{background:#162033;border:1px solid #334155;border-radius:16px;padding:16px}
.service{font-weight:700;margin-bottom:8px}.pill{display:inline-block;padding:4px 8px;border-radius:999px;font-size:12px;font-weight:700}.pill.up{background:#153d2f;color:#86efac}.pill.down{background:#4c1d1d;color:#fca5a5}
.meta{margin-top:10px;color:#cbd5e1;font-size:13px;word-break:break-word}.small{margin-top:8px;color:#94a3b8;font-size:12px;word-break:break-word}.actions{display:flex;gap:12px;flex-wrap:wrap;margin-top:18px}.btn{text-decoration:none;padding:10px 14px;border-radius:10px;background:#38bdf8;color:#082032;font-weight:700}
</style>
</head>
<body>
<div class="shell"><div class="panel"><div class="badge %s">%s</div><h1>Sports Stream Public Health</h1><p>This page is public and shows the live gateway and internal service health without login.</p><div class="grid">%s</div><div class="actions"><a class="btn" href="https://livestream.study/">Open Website</a></div></div></div>
</body>
</html>`, badgeClass, html.EscapeString(badgeText), cards.String())
}

const landingPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sports Stream Backend</title>
<style>
  *{margin:0;padding:0;box-sizing:border-box}
  body{font-family:'Segoe UI',Arial,sans-serif;background:#0f172a;color:#e2e8f0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px}
  .shell{max-width:860px;width:100%;background:#111827;border:1px solid #334155;border-radius:24px;padding:32px;box-shadow:0 20px 70px rgba(0,0,0,.3)}
  .badge{display:inline-block;padding:7px 12px;border-radius:999px;background:#0b3b4a;color:#67e8f9;font-size:12px;font-weight:700;margin-bottom:16px}
  h1{font-size:40px;margin-bottom:10px}.lead{color:#cbd5e1;line-height:1.6;margin-bottom:20px}
  .links{display:flex;gap:12px;flex-wrap:wrap}.btn{display:inline-block;text-decoration:none;padding:12px 16px;border-radius:10px;font-weight:700}.primary{background:#38bdf8;color:#082032}.secondary{background:#162033;color:#e5eefc;border:1px solid #334155}
</style>
</head>
<body>
<div class="shell">
  <div class="badge">Backend gateway</div>
  <h1>Sports Stream Backend</h1>
  <p class="lead">This Cloud Run service powers the Sports Stream platform API. For the public visitor experience, use the main website. For backend status, open the public health page.</p>
  <div class="links">
    <a class="btn primary" href="https://livestream.study/">Open Main Website</a>
    <a class="btn secondary" href="/health">View Public Health</a>
    <a class="btn secondary" href="/api/v1/streams">View Streams API</a>
  </div>
</div>
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

	// Read from env when provided (Cloud Run), otherwise use local defaults.
	userProxy := newProxy(util.Getenv("USER_SERVICE_URL", "http://127.0.0.1:8081"))
	streamProxy := newProxy(util.Getenv("STREAM_SERVICE_URL", "http://127.0.0.1:8082"))
	analyticsProxy := newProxy(util.Getenv("ANALYTICS_SERVICE_URL", "http://127.0.0.1:8085"))
	notificationProxy := newProxy(util.Getenv("NOTIFICATION_SERVICE_URL", "http://127.0.0.1:8083"))
	adminProxy := newProxy(util.Getenv("ADMIN_SERVICE_URL", "http://127.0.0.1:8084"))

	//userProxy := newProxy("http://127.0.0.1:8081")
	//streamProxy := newProxy("http://127.0.0.1:8082")
	//notificationProxy := newProxy("http://127.0.0.1:8083")
	//adminProxy := newProxy("http://127.0.0.1:8084")
	//analyticsProxy := newProxy("http://127.0.0.1:8085")
	healthClient := &http.Client{Timeout: 2 * time.Second}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		log.Printf(`{"gateway":"route","method":%q,"path":%q}`, r.Method, path)

		switch {

		// ── Root → landing page ────────────────────────────────────────────
		case path == "/" || path == "":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(landingPage))

		// ── Health checks ──────────────────────────────────────────────────
		case path == "/health" || path == "/health-api":
			services := map[string]serviceHealth{
				"user":         checkHealth(healthClient, "http://127.0.0.1:8081/health"),
				"stream":       checkHealth(healthClient, "http://127.0.0.1:8082/health"),
				"notification": checkHealth(healthClient, "http://127.0.0.1:8083/health"),
				"admin":        checkHealth(healthClient, "http://127.0.0.1:8084/health"),
				"analytics":    checkHealth(healthClient, "http://127.0.0.1:8085/health"),
			}

			overall := "ok"
			for _, s := range services {
				if s.Status != "up" {
					overall = "degraded"
					break
				}
			}

			statusCode := http.StatusOK
			if overall != "ok" {
				statusCode = http.StatusServiceUnavailable
			}

			accept := strings.ToLower(r.Header.Get("Accept"))
			if strings.Contains(accept, "text/html") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(statusCode)
				_, _ = w.Write([]byte(renderHealthPage(overall, services)))
				break
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"gateway":  overall,
				"services": services,
			})

		case path == "/health/user":
			r.URL.Path = "/health"
			userProxy.ServeHTTP(w, r)

		case path == "/health/stream":
			r.URL.Path = "/health"
			streamProxy.ServeHTTP(w, r)

		case path == "/health/notification":
			r.URL.Path = "/health"
			notificationProxy.ServeHTTP(w, r)

		case path == "/health/admin":
			r.URL.Path = "/health"
			adminProxy.ServeHTTP(w, r)

		case path == "/health/analytics":
			r.URL.Path = "/health"
			analyticsProxy.ServeHTTP(w, r)

		// ── notification-service ───────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/notifications"):
			notificationProxy.ServeHTTP(w, r)

		// ── analytics-service ──────────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/analytics"):
			analyticsProxy.ServeHTTP(w, r)

		// ── admin-service ──────────────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/admin"):
			adminProxy.ServeHTTP(w, r)

		case strings.HasPrefix(path, "/admin"):
			adminProxy.ServeHTTP(w, r)

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
