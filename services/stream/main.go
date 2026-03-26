package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"github.com/gorilla/mux"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────────────
// ViewerRegistry — thread-safe in-memory set of UIDs per stream
// Prevents double-join and double-leave which cause wrong counts
// ─────────────────────────────────────────────────────────────────────────────

type ViewerRegistry struct {
	mu      sync.RWMutex
	viewers map[string]map[string]bool // streamID → set of UIDs
}

func NewViewerRegistry() *ViewerRegistry {
	return &ViewerRegistry{viewers: make(map[string]map[string]bool)}
}

// Add returns true only if uid was NOT already present (new join)
func (r *ViewerRegistry) Add(streamID, uid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.viewers[streamID] == nil {
		r.viewers[streamID] = make(map[string]bool)
	}
	if r.viewers[streamID][uid] {
		return false // already joined — skip Firestore increment
	}
	r.viewers[streamID][uid] = true
	return true
}

// Remove returns true only if uid WAS present (real leave)
func (r *ViewerRegistry) Remove(streamID, uid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.viewers[streamID] == nil || !r.viewers[streamID][uid] {
		return false // not in registry — skip Firestore decrement
	}
	delete(r.viewers[streamID], uid)
	if len(r.viewers[streamID]) == 0 {
		delete(r.viewers, streamID)
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler
// ─────────────────────────────────────────────────────────────────────────────

type handler struct {
	fs       *firestore.Client
	auth     *auth.Client
	registry *ViewerRegistry
}

type Stream struct {
	ID             string    `firestore:"id"             json:"id"`
	Title          string    `firestore:"title"          json:"title"`
	HlsUrl         string    `firestore:"hlsUrl"         json:"hlsUrl"`
	Status         string    `firestore:"status"         json:"status"`
	ViewerCount    int       `firestore:"viewerCount"    json:"viewerCount"`
	BroadcasterUID string    `firestore:"broadcasterUid" json:"broadcasterUid"`
	CreatedAt      time.Time `firestore:"createdAt"      json:"createdAt"`
	UpdatedAt      time.Time `firestore:"updatedAt"      json:"updatedAt"`
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
}

func (h *handler) uidFromToken(r *http.Request) (string, error) {
	token := r.Header.Get("Authorization")
	if len(token) > 7 && token[:7] == "Bearer " {
		token = token[7:]
	}
	t, err := h.auth.VerifyIDToken(r.Context(), token)
	if err != nil {
		return "", err
	}
	return t.UID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// joinStream
// BUG FIX: registry prevents double-join → no double increment in Firestore
// BUG FIX: uses transaction so concurrent joins are safe
// ─────────────────────────────────────────────────────────────────────────────

func (h *handler) joinStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, err := h.uidFromToken(r)
	if err != nil {
		jsonErr(w, 401, "unauthorized")
		return
	}

	added := h.registry.Add(streamID, uid)
	if !added {
		// Already in registry — return current count, don't touch Firestore
		snap, err := h.fs.Collection("streams").Doc(streamID).Get(r.Context())
		if err != nil {
			jsonErr(w, 404, "stream not found")
			return
		}
		var s Stream
		snap.DataTo(&s)
		if s.ViewerCount < 0 {
			s.ViewerCount = 0
		}
		jsonOK(w, map[string]any{"viewerCount": s.ViewerCount, "joined": false})
		return
	}

	ref := h.fs.Collection("streams").Doc(streamID)
	var newCount int

	err = h.fs.RunTransaction(r.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if err != nil {
			return err
		}
		var s Stream
		snap.DataTo(&s)
		newCount = s.ViewerCount + 1
		return tx.Update(ref, []firestore.Update{
			{Path: "viewerCount", Value: newCount},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
	})

	if err != nil {
		h.registry.Remove(streamID, uid) // rollback registry
		jsonErr(w, 500, "failed to join stream")
		return
	}

	jsonOK(w, map[string]any{"viewerCount": newCount, "joined": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// leaveStream
// BUG FIX 1: registry prevents double-leave → no double decrement
// BUG FIX 2: transaction with max(count-1, 0) → never goes negative
// ─────────────────────────────────────────────────────────────────────────────

func (h *handler) leaveStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, err := h.uidFromToken(r)
	if err != nil {
		jsonErr(w, 401, "unauthorized")
		return
	}

	removed := h.registry.Remove(streamID, uid)
	if !removed {
		// Not in registry — already left, don't touch Firestore
		jsonOK(w, map[string]any{"viewerCount": 0, "left": false})
		return
	}

	ref := h.fs.Collection("streams").Doc(streamID)
	var newCount int

	err = h.fs.RunTransaction(r.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil // stream deleted — nothing to update
			}
			return err
		}
		var s Stream
		snap.DataTo(&s)

		// *** FLOOR AT 0 — THIS IS THE KEY FIX FOR NEGATIVE COUNTS ***
		newCount = s.ViewerCount - 1
		if newCount < 0 {
			newCount = 0
		}

		return tx.Update(ref, []firestore.Update{
			{Path: "viewerCount", Value: newCount},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
	})

	if err != nil {
		jsonErr(w, 500, "failed to leave stream")
		return
	}

	jsonOK(w, map[string]any{"viewerCount": newCount, "left": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// listStreams — returns all live streams, floors negative counts at 0
// ─────────────────────────────────────────────────────────────────────────────

func (h *handler) listStreams(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("streams").
		Where("status", "==", "live").
		OrderBy("createdAt", firestore.Desc).
		Documents(r.Context()).GetAll()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}

	streams := make([]Stream, 0, len(docs))
	for _, d := range docs {
		var s Stream
		d.DataTo(&s)
		s.ID = d.Ref.ID
		if s.ViewerCount < 0 {
			s.ViewerCount = 0
			go fixNegativeCount(h.fs, s.ID) // auto-heal in background
		}
		streams = append(streams, s)
	}

	jsonOK(w, streams)
}

func (h *handler) getStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("streams").Doc(streamID).Get(r.Context())
	if err != nil {
		jsonErr(w, 404, "stream not found")
		return
	}
	var s Stream
	snap.DataTo(&s)
	s.ID = snap.Ref.ID
	if s.ViewerCount < 0 {
		s.ViewerCount = 0
	}
	jsonOK(w, s)
}

func (h *handler) getManifest(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("streams").Doc(streamID).Get(r.Context())
	if err != nil {
		jsonErr(w, 404, "stream not found")
		return
	}
	var s Stream
	snap.DataTo(&s)
	if s.HlsUrl == "" {
		jsonErr(w, 404, "no manifest available")
		return
	}
	jsonOK(w, map[string]string{"manifestUrl": s.HlsUrl, "hlsUrl": s.HlsUrl})
}

// resetViewerCount — admin endpoint to fix a specific stream's count
// POST /api/v1/streams/{id}/reset-viewers
func (h *handler) resetViewerCount(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	h.registry.mu.Lock()
	delete(h.registry.viewers, streamID)
	h.registry.mu.Unlock()
	fixNegativeCount(h.fs, streamID)
	jsonOK(w, map[string]any{"viewerCount": 0, "reset": true})
}

// fixAllNegative — admin endpoint to scan and fix ALL streams
// POST /api/v1/admin/fix-viewer-counts
func (h *handler) fixAllNegative(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("streams").Documents(r.Context()).GetAll()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	fixed := 0
	for _, d := range docs {
		var s Stream
		d.DataTo(&s)
		if s.ViewerCount < 0 {
			fixNegativeCount(h.fs, d.Ref.ID)
			fixed++
		}
	}
	jsonOK(w, map[string]any{"fixed": fixed})
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{"status": "ok", "service": "stream-service"})
}

// fixNegativeCount sets viewerCount to 0 in Firestore
func fixNegativeCount(fs *firestore.Client, streamID string) {
	fs.Collection("streams").Doc(streamID).Update(
		context.Background(),
		[]firestore.Update{{Path: "viewerCount", Value: 0}},
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	credFile := os.Getenv("FIREBASE_CREDENTIALS")
	projectID := os.Getenv("GCP_PROJECT_ID")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}

	var app *firebase.App
	var err error
	if credFile != "" {
		app, err = firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID},
			option.WithCredentialsFile(credFile))
	} else {
		app, err = firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID})
	}
	if err != nil {
		log.Fatalf("firebase init: %v", err)
	}

	authClient, _ := app.Auth(ctx)
	fsClient, err := app.Firestore(ctx)
	if err != nil {
		log.Fatalf("firestore: %v", err)
	}
	defer fsClient.Close()

	h := &handler{
		fs:       fsClient,
		auth:     authClient,
		registry: NewViewerRegistry(),
	}

	r := mux.NewRouter()
	r.HandleFunc("/health", h.health).Methods("GET")
	r.HandleFunc("/api/v1/streams", h.listStreams).Methods("GET")
	r.HandleFunc("/api/v1/streams/{id}", h.getStream).Methods("GET")
	r.HandleFunc("/api/v1/streams/{id}/join", h.joinStream).Methods("POST")
	r.HandleFunc("/api/v1/streams/{id}/leave", h.leaveStream).Methods("POST")
	r.HandleFunc("/api/v1/streams/{id}/manifest", h.getManifest).Methods("GET")
	r.HandleFunc("/api/v1/streams/{id}/reset-viewers", h.resetViewerCount).Methods("POST")
	r.HandleFunc("/api/v1/admin/fix-viewer-counts", h.fixAllNegative).Methods("POST")

	log.Printf("stream-service :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}
