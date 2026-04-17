package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gorilla/mux"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fbclient "sports-stream-backend/pkg/firebase"
	"sports-stream-backend/pkg/middleware"
	"sports-stream-backend/pkg/util"
)

// ────────────────────────────────────────────────────────────────────────────
// Models
// ────────────────────────────────────────────────────────────────────────────

// UserProfile matches the Android UserProfile data class exactly.
// Role is set to "viewer" by default on first login.
// Admin can change role to "broadcaster" or "admin" in Firebase Console.
type UserProfile struct {
	UID         string    `firestore:"uid"         json:"uid"`
	Email       string    `firestore:"email"       json:"email"`
	DisplayName string    `firestore:"displayName" json:"displayName"`
	PhotoURL    string    `firestore:"photoUrl"    json:"photoUrl"`
	FavTeams    []string  `firestore:"favTeams"    json:"favTeams"`
	Role        string    `firestore:"role"        json:"role"` // viewer | broadcaster | admin
	CreatedAt   time.Time `firestore:"createdAt"   json:"createdAt"`
	UpdatedAt   time.Time `firestore:"updatedAt"   json:"updatedAt"`
}

// ────────────────────────────────────────────────────────────────────────────
// Handler
// ────────────────────────────────────────────────────────────────────────────

type handler struct {
	fs *firestore.Client
}

// POST /api/v1/auth/verify
//
// Android sends: { "idToken": "<firebase-jwt>" } in the request body.
// On success, upserts the user profile in Firestore and returns the profile.
// New users get role: "viewer" by default.
// Admin can promote users to "broadcaster" or "admin" in Firebase Console.
func (h *handler) verifyToken(w http.ResponseWriter, r *http.Request) {
	var rawToken string

	var reqBody struct {
		IDToken string `json:"idToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err == nil && reqBody.IDToken != "" {
		rawToken = reqBody.IDToken
	} else {
		rawToken = extractBearer(r)
	}

	if rawToken == "" {
		jsonError(w, "missing token — provide idToken in body or Authorization: Bearer header", http.StatusUnauthorized)
		return
	}

	token, err := fbclient.VerifyIDToken(r.Context(), rawToken)
	if err != nil {
		jsonError(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}

	uid := token.UID
	now := time.Now().UTC()

	// Firebase JWT claims:
	// "name"    → displayName (set during signUp with displayName field)
	// "picture" → photoUrl   (set by Google Sign-In)
	// "email"   → email
	profile := UserProfile{
		UID:         uid,
		Email:       stringClaim(token.Claims, "email"),
		DisplayName: stringClaim(token.Claims, "name"), // ← key is "name" not "displayName"
		PhotoURL:    stringClaim(token.Claims, "picture"),
		UpdatedAt:   now,
	}

	ref := h.fs.Collection("users").Doc(uid)
	existingSnap, getErr := ref.Get(r.Context())

	if status.Code(getErr) == codes.NotFound {
		// ── New user — create full document ───────────────────────────────
		profile.CreatedAt = now
		profile.Role = "viewer"
		profile.FavTeams = []string{}

		if _, err = ref.Set(r.Context(), profile); err != nil {
			log.Printf("firestore set: %v", err)
			jsonError(w, "failed to create profile", http.StatusInternalServerError)
			return
		}
		log.Printf(`{"service":"user-service","level":"info","msg":"new user created","uid":%q,"displayName":%q}`,
			uid, profile.DisplayName)

	} else {
		// ── Existing user — update fields, preserve role ───────────────────
		updates := []firestore.Update{
			{Path: "email", Value: profile.Email},
			{Path: "displayName", Value: profile.DisplayName}, // ← always sync displayName
			{Path: "photoUrl", Value: profile.PhotoURL},
			{Path: "updatedAt", Value: profile.UpdatedAt},
		}

		// If existing user has no role — backfill with "viewer"
		var existingProfile UserProfile
		if getErr == nil {
			existingSnap.DataTo(&existingProfile)
			if existingProfile.Role == "" {
				updates = append(updates, firestore.Update{Path: "role", Value: "viewer"})
			}
		}

		if _, err = ref.Update(r.Context(), updates); err != nil {
			log.Printf("firestore update: %v", err)
			jsonError(w, "failed to update profile", http.StatusInternalServerError)
			return
		}

		// Read back full profile so Android gets current role and all fields
		if snap, snapErr := ref.Get(r.Context()); snapErr == nil {
			snap.DataTo(&profile)
		}
	}

	jsonOK(w, profile)
}

// GET /api/v1/users/me
func (h *handler) getMe(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())

	snap, err := h.fs.Collection("users").Doc(uid).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "profile not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "firestore error", http.StatusInternalServerError)
		return
	}

	var p UserProfile
	if err := snap.DataTo(&p); err != nil {
		jsonError(w, "decode error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, p)
}

// PATCH /api/v1/users/me
// Updates displayName, favTeams, and fcmToken (any subset).
// Does NOT allow updating role — role is managed by admin in Firebase Console.
func (h *handler) updateMe(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())

	var body struct {
		DisplayName string   `json:"displayName"`
		FavTeams    []string `json:"favTeams"`
		FCMToken    string   `json:"fcmToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	updates := []firestore.Update{
		{Path: "updatedAt", Value: time.Now().UTC()},
	}
	if body.DisplayName != "" {
		updates = append(updates, firestore.Update{Path: "displayName", Value: body.DisplayName})
	}
	if body.FavTeams != nil {
		updates = append(updates, firestore.Update{Path: "favTeams", Value: body.FavTeams})
	}
	if body.FCMToken != "" {
		updates = append(updates, firestore.Update{Path: "fcmToken", Value: body.FCMToken})
		log.Printf(`{"service":"user-service","level":"info","msg":"fcm token updated","uid":%q}`, uid)
	}

	if _, err := h.fs.Collection("users").Doc(uid).Update(r.Context(), updates); err != nil {
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, nil)
}

// ────────────────────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.ProjectID()
	port := util.Getenv("PORT", "8081")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")

	// Firebase Admin SDK
	if _, err := fbclient.InitClient(ctx, credsFile); err != nil {
		log.Fatalf("firebase init: %v", err)
	}

	// Firestore — pass credentials explicitly
	var fsOpts []option.ClientOption
	if credsFile != "" {
		if util.LooksLikeJSONCredential(credsFile) {
			fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(credsFile)))
		} else if util.FileExists(credsFile) {
			fsOpts = append(fsOpts, option.WithCredentialsFile(credsFile))
		} else {
			log.Printf("user-service: credential file %q not found; falling back to default credentials", credsFile)
		}
	}
	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("firestore init: %v", err)
	}
	defer fs.Close()

	h := &handler{fs: fs}

	r := mux.NewRouter()

	// Health check — no auth
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := fs.Collection("users").Limit(1).Documents(healthCtx).Next()
		if err != nil && err != iterator.Done {
			jsonError(w, "health check failed: firestore unreachable", http.StatusServiceUnavailable)
			return
		}

		jsonOK(w, map[string]string{"service": "user-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()

	// Public — Android calls this after Google Sign-In
	v1.HandleFunc("/auth/verify", h.verifyToken).Methods(http.MethodPost)

	// Protected — require Firebase Bearer token
	me := v1.PathPrefix("/users/me").Subrouter()
	me.Use(middleware.AuthRequired)
	me.HandleFunc("", h.getMe).Methods(http.MethodGet)
	me.HandleFunc("", h.updateMe).Methods(http.MethodPatch)

	log.Printf("user-service listening on :%s  (routes: /api/v1/auth/verify, /api/v1/users/me)", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data":    v,
	})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"message": msg,
	})
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return h[7:]
	}
	return ""
}

// stringClaim safely extracts a string from Firebase JWT token claims.
// Firebase uses "name" for displayName and "picture" for photoUrl.
func stringClaim(claims map[string]any, key string) string {
	if v, ok := claims[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
