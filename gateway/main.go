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
// Health endpoints:
//   GET /health              → gateway itself
//   GET /health/user         → user-service    :8081
//   GET /health/stream       → stream-service  :8082
//   GET /health/analytics    → analytics       :8085
//
// API routes:
//   /api/v1/auth/*           → user-service    :8081
//   /api/v1/users/*          → user-service    :8081
//   /api/v1/streams*         → stream-service  :8082
//   /api/v1/analytics/*      → analytics       :8085
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
	analyticsProxy := newProxy("http://127.0.0.1:8085")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		log.Printf(`{"gateway":"route","method":%q,"path":%q}`, r.Method, path)

		switch {

		// ── Health checks per service ──────────────────────────────────
		case path == "/health":
			// Gateway health — all services listed
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
  "gateway": "ok",
  "services": {
    "user":         "http://127.0.0.1:8081/health",
    "stream":       "http://127.0.0.1:8082/health",
    "analytics":    "http://127.0.0.1:8085/health",
    "notification": "pub/sub only - no http"
  }
}`))

		case path == "/health/user":
			// Proxy to user-service health
			r.URL.Path = "/health"
			userProxy.ServeHTTP(w, r)

		case path == "/health/stream":
			// Proxy to stream-service health
			r.URL.Path = "/health"
			streamProxy.ServeHTTP(w, r)

		case path == "/health/analytics":
			// Proxy to analytics-service health
			r.URL.Path = "/health"
			analyticsProxy.ServeHTTP(w, r)

		// ── analytics-service ──────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/analytics"):
			analyticsProxy.ServeHTTP(w, r)

		// ── stream-service ─────────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/streams"):
			streamProxy.ServeHTTP(w, r)

		// ── user-service ───────────────────────────────────────────────
		case strings.HasPrefix(path, "/api/v1/auth"):
			userProxy.ServeHTTP(w, r)

		case strings.HasPrefix(path, "/api/v1/users"):
			userProxy.ServeHTTP(w, r)

		// ── default → user-service ─────────────────────────────────────
		default:
			userProxy.ServeHTTP(w, r)
		}
	})

	log.Printf(`{"gateway":"started","port":%q,"routes":["health","health/user","health/stream","health/analytics","api/v1/auth","api/v1/users","api/v1/streams","api/v1/analytics"]}`, port)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("gateway: %v", err)
	}
}
