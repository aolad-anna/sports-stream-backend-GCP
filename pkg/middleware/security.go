package middleware

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// SecurityHeaders ── Security Headers ──────────────────────────────────────────────────────────
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// CORS ── CORS ──────────────────────────────────────────────────────────────────────
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Android app sends no Origin header — allow all
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Rate Limiter ──────────────────────────────────────────────────────────────
// 100 requests per minute per IP — protects against abuse on join/leave endpoints.

type rateLimiter struct {
	requests map[string][]time.Time
}

var limiter = &rateLimiter{requests: make(map[string][]time.Time)}

func RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		ip := strings.Split(r.RemoteAddr, ":")[0]
		now := time.Now()
		window := now.Add(-1 * time.Minute)

		var recent []time.Time
		for _, t := range limiter.requests[ip] {
			if t.After(window) {
				recent = append(recent, t)
			}
		}
		recent = append(recent, now)
		limiter.requests[ip] = recent

		if len(recent) > 100 {
			log.Printf("rate limit exceeded ip=%s path=%s", ip, r.URL.Path)
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"success":false,"message":"rate limit exceeded"}`,
				http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Request Logger ────────────────────────────────────────────────────────────

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{w, http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Printf(`{"method":%q,"path":%q,"status":%d,"duration":%q}`,
			r.Method, r.URL.Path, rw.status, time.Since(start).String())
	})
}
