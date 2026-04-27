package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthRequired_MissingAuthorizationHeader(t *testing.T) {
	h := AuthRequired(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "missing Authorization: Bearer header") {
		t.Fatalf("expected missing bearer message, got %q", rr.Body.String())
	}
}

func TestAuthRequired_InvalidAuthorizationHeaderFormat(t *testing.T) {
	h := AuthRequired(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Token abc123")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "missing Authorization: Bearer header") {
		t.Fatalf("expected missing bearer message, got %q", rr.Body.String())
	}
}
