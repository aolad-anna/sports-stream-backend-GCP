package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gorilla/mux"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fbclient "sports-stream-backend/pkg/firebase"
	"sports-stream-backend/pkg/middleware"
	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

// ────────────────────────────────────────────────────────────────────────────
// Models — field names match Android Stream.kt exactly
// ────────────────────────────────────────────────────────────────────────────

type Stream struct {
	ID             string    `firestore:"id"             json:"id"`
	Title          string    `firestore:"title"          json:"title"`
	Status         string    `firestore:"status"         json:"status"`
	HLSUrl         string    `firestore:"hlsUrl"         json:"hlsUrl"`
	ViewerCount    int       `firestore:"viewerCount"    json:"viewerCount"`
	BroadcasterUID string    `firestore:"broadcasterUid" json:"broadcasterUid"`
	CreatedAt      time.Time `firestore:"createdAt"      json:"createdAt"`
	UpdatedAt      time.Time `firestore:"updatedAt"      json:"updatedAt"`
}

type CreateStreamRequest struct {
	Title   string `json:"title"`
	RTMPUrl string `json:"rtmpUrl"`
}

// ────────────────────────────────────────────────────────────────────────────
// ViewerRegistry — in-memory per-pod viewer tracking
// ────────────────────────────────────────────────────────────────────────────

type ViewerRegistry struct {
	mu      sync.RWMutex
	viewers map[string]map[string]bool // streamID -> set of UIDs
}

func newViewerRegistry() *ViewerRegistry {
	return &ViewerRegistry{viewers: make(map[string]map[string]bool)}
}

// Add returns true if uid was not already present (idempotent — prevents double-counting).
func (r *ViewerRegistry) Add(streamID, uid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.viewers[streamID] == nil {
		r.viewers[streamID] = make(map[string]bool)
	}
	if r.viewers[streamID][uid] {
		return false
	}
	r.viewers[streamID][uid] = true
	return true
}

// Remove returns true if uid was present.
func (r *ViewerRegistry) Remove(streamID, uid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewers[streamID][uid] {
		return false
	}
	delete(r.viewers[streamID], uid)
	return true
}

func (r *ViewerRegistry) Count(streamID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.viewers[streamID])
}

// ────────────────────────────────────────────────────────────────────────────
// Handler
// ────────────────────────────────────────────────────────────────────────────

type handler struct {
	fs       *firestore.Client
	registry *ViewerRegistry
}

// getUserRole reads the role field from Firestore users/{uid}.
// Returns empty string if user not found or role not set.
func (h *handler) getUserRole(ctx context.Context, uid string) string {
	snap, err := h.fs.Collection("users").Doc(uid).Get(ctx)
	if err != nil {
		return ""
	}
	var profile struct {
		Role string `firestore:"role"`
	}
	snap.DataTo(&profile)
	return profile.Role
}

// POST /api/v1/streams  (protected — broadcaster or admin only)
// Viewers get 403 Forbidden.
// Admin sets role to "broadcaster" in Firebase Console to allow stream creation.
func (h *handler) createStream(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())

	// ── Role enforcement ───────────────────────────────────────────────────
	role := h.getUserRole(r.Context(), uid)
	if role != "broadcaster" && role != "admin" {
		log.Printf(`{"service":"stream-service","level":"warn","msg":"forbidden","uid":%q,"role":%q}`, uid, role)
		jsonError(w, "only broadcasters can create streams", http.StatusForbidden)
		return
	}

	var req CreateStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
		jsonError(w, "title is required", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	streamID := fmt.Sprintf("stream_%d", now.UnixMilli())

	stream := Stream{
		ID:             streamID,
		Title:          req.Title,
		Status:         "live",
		HLSUrl:         "",
		ViewerCount:    0,
		BroadcasterUID: uid,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if _, err := h.fs.Collection("streams").Doc(streamID).Set(r.Context(), stream); err != nil {
		log.Printf("firestore create stream: %v", err)
		jsonError(w, "failed to create stream", http.StatusInternalServerError)
		return
	}

	go startPackager(streamID, req.RTMPUrl, h.fs)

	go psclient.PublishEvent(context.Background(), "stream_events", map[string]any{
		"eventType": "stream_started",
		"streamId":  streamID,
		"title":     req.Title,
		"uid":       uid,
		"timestamp": now.Format(time.RFC3339),
	})

	log.Printf(`{"service":"stream-service","level":"info","msg":"stream created","streamId":%q,"uid":%q,"role":%q}`, streamID, uid, role)
	jsonOK(w, stream)
}

// GET /api/v1/streams  (public)
// Android HomeViewModel loads this to populate the stream list.
func (h *handler) listStreams(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("streams").
		Where("status", "==", "live").
		Limit(50).
		Documents(r.Context()).GetAll()

	if err != nil {
		log.Printf("firestore list streams: %v", err)
		jsonError(w, "failed to fetch streams", http.StatusInternalServerError)
		return
	}

	streams := make([]Stream, 0, len(docs))
	for _, d := range docs {
		var s Stream
		if err := d.DataTo(&s); err == nil {
			streams = append(streams, s)
		}
	}
	jsonOK(w, streams)
}

// GET /api/v1/streams/:id  (public)
func (h *handler) getStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("streams").Doc(streamID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "stream not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "firestore error", http.StatusInternalServerError)
		return
	}
	var s Stream
	if err := snap.DataTo(&s); err != nil {
		jsonError(w, "decode error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, s)
}

// POST /api/v1/streams/:id/join  (protected)
func (h *handler) joinStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	if added := h.registry.Add(streamID, uid); !added {
		jsonOK(w, map[string]any{"joined": true, "viewerCount": h.registry.Count(streamID)})
		return
	}

	_, err := h.fs.Collection("streams").Doc(streamID).Update(r.Context(), []firestore.Update{
		{Path: "viewerCount", Value: firestore.Increment(1)},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})
	if err != nil {
		h.registry.Remove(streamID, uid)
		jsonError(w, "failed to join stream", http.StatusInternalServerError)
		return
	}

	go psclient.PublishEvent(context.Background(), "viewer_events", map[string]any{
		"eventType": "viewer_join",
		"streamId":  streamID,
		"uid":       uid,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})

	jsonOK(w, map[string]any{"joined": true, "viewerCount": h.registry.Count(streamID)})
}

// POST /api/v1/streams/:id/leave  (protected)
func (h *handler) leaveStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	if removed := h.registry.Remove(streamID, uid); !removed {
		jsonOK(w, map[string]any{"left": true})
		return
	}

	h.fs.Collection("streams").Doc(streamID).Update(r.Context(), []firestore.Update{
		{Path: "viewerCount", Value: firestore.Increment(-1)},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	go psclient.PublishEvent(context.Background(), "viewer_events", map[string]any{
		"eventType": "viewer_leave",
		"streamId":  streamID,
		"uid":       uid,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})

	jsonOK(w, map[string]any{"left": true})
}

// GET /api/v1/streams/:id/manifest  (protected)
func (h *handler) getManifest(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("streams").Doc(streamID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "stream not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "firestore error", http.StatusInternalServerError)
		return
	}
	var s Stream
	snap.DataTo(&s)
	if s.HLSUrl == "" {
		jsonError(w, "stream not ready yet", http.StatusAccepted)
		return
	}
	jsonOK(w, map[string]string{"streamId": streamID, "manifestUrl": s.HLSUrl})
}

// ────────────────────────────────────────────────────────────────────────────
// FFmpeg HLS packager — runs in a goroutine, never blocks HTTP
// ────────────────────────────────────────────────────────────────────────────

func startPackager(streamID, rtmpURL string, fs *firestore.Client) {
	gcsBucket := util.Getenv("GCS_BUCKET", "sports-stream-66553.appspot.com")
	cdnBase := util.Getenv("CDN_BASE_URL", "https://storage.googleapis.com/"+gcsBucket)
	outDir := fmt.Sprintf("/tmp/hls/%s", streamID)

	args := []string{
		"-i", rtmpURL,
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-map", "0:v", "-map", "0:a", "-s:v:0", "1280x720", "-b:v:0", "1500k",
		"-map", "0:v", "-map", "0:a", "-s:v:1", "854x480", "-b:v:1", "800k",
		"-map", "0:v", "-map", "0:a", "-s:v:2", "640x360", "-b:v:2", "400k",
		"-c:a", "aac", "-b:a", "128k",
		"-f", "hls", "-hls_time", "2", "-hls_list_size", "5",
		"-hls_flags", "delete_segments+append_list",
		"-hls_segment_filename", outDir + "/seg_%v_%03d.ts",
		"-master_pl_name", "index.m3u8",
		"-var_stream_map", "v:0,a:0 v:1,a:1 v:2,a:2",
		outDir + "/index.m3u8",
	}

	cmd := exec.Command("ffmpeg", args...)
	if err := cmd.Start(); err != nil {
		log.Printf(`{"service":"stream-service","level":"error","msg":"ffmpeg start failed","streamId":%q,"error":%q}`, streamID, err.Error())
		return
	}

	hlsURL := fmt.Sprintf("%s/%s/index.m3u8", cdnBase, streamID)
	ctx := context.Background()
	fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
		{Path: "hlsUrl", Value: hlsURL},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	log.Printf(`{"service":"stream-service","level":"info","msg":"packager started","streamId":%q}`, streamID)

	if err := cmd.Wait(); err != nil {
		log.Printf(`{"service":"stream-service","level":"warn","msg":"ffmpeg exited","streamId":%q}`, streamID)
	}

	fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
		{Path: "status", Value: "ended"},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	psclient.PublishEvent(ctx, "stream_events", map[string]any{
		"eventType": "stream_ended",
		"streamId":  streamID,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8082")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")

	if _, err := fbclient.InitClient(ctx, credsFile); err != nil {
		log.Fatalf("firebase init: %v", err)
	}

	if _, err := psclient.InitClient(ctx, projectID, credsFile); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}

	var fsOpts []option.ClientOption
	if credsFile != "" {
		if strings.HasPrefix(strings.TrimSpace(credsFile), "{") {
			fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(credsFile)))
		} else {
			fsOpts = append(fsOpts, option.WithCredentialsFile(credsFile))
		}
	}
	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("firestore init: %v", err)
	}
	defer fs.Close()

	h := &handler{fs: fs, registry: newViewerRegistry()}

	r := mux.NewRouter()
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, map[string]string{"service": "stream-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()

	// Public
	v1.HandleFunc("/streams", h.listStreams).Methods(http.MethodGet)
	v1.HandleFunc("/streams/{id}", h.getStream).Methods(http.MethodGet)

	// Protected
	protected := v1.NewRoute().Subrouter()
	protected.Use(middleware.AuthRequired)
	protected.HandleFunc("/streams", h.createStream).Methods(http.MethodPost)
	protected.HandleFunc("/streams/{id}/join", h.joinStream).Methods(http.MethodPost)
	protected.HandleFunc("/streams/{id}/leave", h.leaveStream).Methods(http.MethodPost)
	protected.HandleFunc("/streams/{id}/manifest", h.getManifest).Methods(http.MethodGet)

	log.Printf("stream-service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
}
