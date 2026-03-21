package middleware

import (
	"context"
	"net/http"
	"strings"

	fbclient "sports-stream-backend/pkg/firebase"
)

type contextKey string

const uidKey contextKey = "uid"

// AuthRequired is a net/http middleware that validates the Firebase ID token
// from the Authorization: Bearer <token> header.
// On success it stores the verified UID in the request context.
// On failure it returns 401 with a JSON error body.
func AuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		if len(raw) < 8 || !strings.EqualFold(raw[:7], "bearer ") {
			writeJSON(w, http.StatusUnauthorized, `{"success":false,"message":"missing Authorization: Bearer header"}`)
			return
		}
		idToken := raw[7:]

		token, err := fbclient.VerifyIDToken(r.Context(), idToken)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, `{"success":false,"message":"invalid or expired token"}`)
			return
		}

		ctx := context.WithValue(r.Context(), uidKey, token.UID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UIDFromContext reads the UID stored by AuthRequired.
// Returns ("", false) if not present.
func UIDFromContext(ctx context.Context) (string, bool) {
	uid, ok := ctx.Value(uidKey).(string)
	return uid, ok
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write([]byte(body))
}
