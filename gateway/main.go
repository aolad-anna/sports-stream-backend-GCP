package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

// ────────────────────────────────────────────────────────────────────────────
// Sports Stream API Gateway — pure Go reverse proxy
//
// Health:
//   GET /health                    → gateway status + all services listed
//   GET /health/user               → user-service        :8081
//   GET /health/stream             → stream-service      :8082
//   GET /health/analytics          → analytics-service   :8085
//   GET /health/notification       → notification-service :8083
//
// API routes:
//   /api/v1/auth/*                 → user-service        :8081
//   /api/v1/users/*                → user-service        :8081
//   /api/v1/streams*               → stream-service      :8082
//   /api/v1/analytics/*            → analytics-service   :8085
//   /api/v1/notifications/*        → notification-service :8083
// ────────────────────────────────────────────────────────────────────────────

func newProxy(target string) *httputil.ReverseProxy {
	u, _ := url.Parse(target)
	return httputil.NewSingleHostReverseProxy(u)
}

func main() {
	port := os.Getenv("GATEWAY_PORT")
	if port == "" {
		port = "8080"
	}

	// Internal service proxies
	userProxy := newProxy("http://127.0.0.1:8081")
	streamProxy := newProxy("http://127.0.0.1:8082")
	notificationProxy := newProxy("http://127.0.0.1:8083")
	analyticsProxy := newProxy("http://127.0.0.1:8085")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		log.Printf(`{"gateway":"route","method":%q,"path":%q}`, r.Method, path)

		switch {

		// ── Health checks ──────────────────────────────────────────────────
		case path == "/health":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
  "gateway": "ok",
  "services": {
    "user":         "http://127.0.0.1:8081",
    "stream":       "http://127.0.0.1:8082",
    "notification": "http://127.0.0.1:8083",
    "analytics":    "http://127.0.0.1:8085"
  }
}`))

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
		// POST /api/v1/notifications/test  ← test endpoint from Postman
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
