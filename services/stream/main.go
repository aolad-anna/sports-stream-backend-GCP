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
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fbclient "sports-stream-backend/pkg/firebase"
	"sports-stream-backend/pkg/middleware"
	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

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

// ── ViewerRegistry ────────────────────────────────────────────────────────────

type ViewerRegistry struct {
	mu      sync.RWMutex
	viewers map[string]map[string]bool
}

func newViewerRegistry() *ViewerRegistry {
	return &ViewerRegistry{viewers: make(map[string]map[string]bool)}
}

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

func (r *ViewerRegistry) ClearStream(streamID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.viewers, streamID)
}

// ── Handler ───────────────────────────────────────────────────────────────────

type handler struct {
	fs       *firestore.Client
	registry *ViewerRegistry
}

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

func (h *handler) createStream(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())
	role := h.getUserRole(r.Context(), uid)
	if role != "broadcaster" && role != "admin" {
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
		ID: streamID, Title: req.Title, Status: "live",
		ViewerCount: 0, BroadcasterUID: uid, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.fs.Collection("streams").Doc(streamID).Set(r.Context(), stream); err != nil {
		jsonError(w, "failed to create stream", http.StatusInternalServerError)
		return
	}
	go startPackager(streamID, req.RTMPUrl, h.fs)
	go psclient.PublishEvent(context.Background(), "stream_events", map[string]any{
		"eventType": "stream_started", "streamId": streamID,
		"title": req.Title, "uid": uid, "timestamp": now.Format(time.RFC3339),
	})
	jsonOK(w, stream)
}

func (h *handler) listStreams(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("streams").Where("status", "==", "live").
		Limit(50).Documents(r.Context()).GetAll()
	if err != nil {
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

func (h *handler) joinStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	if added := h.registry.Add(streamID, uid); !added {
		jsonOK(w, map[string]any{"joined": true, "viewerCount": h.registry.Count(streamID)})
		return
	}

	ref := h.fs.Collection("streams").Doc(streamID)
	var newCount int
	err := h.fs.RunTransaction(r.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
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
		h.registry.Remove(streamID, uid)
		jsonError(w, "failed to join stream", http.StatusInternalServerError)
		return
	}

	go psclient.PublishEvent(context.Background(), "viewer_events", map[string]any{
		"eventType": "viewer_join", "streamId": streamID,
		"uid": uid, "timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	jsonOK(w, map[string]any{"joined": true, "viewerCount": newCount})
}

func (h *handler) leaveStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	if removed := h.registry.Remove(streamID, uid); !removed {
		jsonOK(w, map[string]any{"left": true})
		return
	}

	ref := h.fs.Collection("streams").Doc(streamID)
	err := h.fs.RunTransaction(r.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if err != nil {
			return err
		}
		var s Stream
		snap.DataTo(&s)
		newCount := s.ViewerCount - 1
		if newCount < 0 {
			newCount = 0
		}
		return tx.Update(ref, []firestore.Update{
			{Path: "viewerCount", Value: newCount},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
	})
	if err != nil {
		log.Printf("leaveStream transaction error streamId=%s: %v", streamID, err)
	}

	go psclient.PublishEvent(context.Background(), "viewer_events", map[string]any{
		"eventType": "viewer_leave", "streamId": streamID,
		"uid": uid, "timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	jsonOK(w, map[string]any{"left": true})
}

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

func (h *handler) resetViewerCount(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())
	if h.getUserRole(r.Context(), uid) != "admin" {
		jsonError(w, "admin only", http.StatusForbidden)
		return
	}
	_, err := h.fs.Collection("streams").Doc(streamID).Update(r.Context(), []firestore.Update{
		{Path: "viewerCount", Value: 0},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})
	if err != nil {
		jsonError(w, "failed to reset", http.StatusInternalServerError)
		return
	}
	h.registry.ClearStream(streamID)
	jsonOK(w, map[string]any{"reset": true, "viewerCount": 0})
}

// ── FFmpeg packager ───────────────────────────────────────────────────────────

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
		log.Printf("ffmpeg start failed streamId=%s: %v", streamID, err)
		return
	}

	hlsURL := fmt.Sprintf("%s/%s/index.m3u8", cdnBase, streamID)
	ctx := context.Background()
	fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
		{Path: "hlsUrl", Value: hlsURL},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	if err := cmd.Wait(); err != nil {
		log.Printf("ffmpeg exited streamId=%s", streamID)
	}

	fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
		{Path: "status", Value: "ended"},
		{Path: "viewerCount", Value: 0},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	psclient.PublishEvent(ctx, "stream_events", map[string]any{
		"eventType": "stream_ended",
		"streamId":  streamID,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8082")
	creds := util.Getenv("FIREBASE_CREDENTIALS", "")

	// Firebase Admin SDK
	if _, err := fbclient.InitClient(ctx, creds); err != nil {
		log.Fatalf("firebase init: %v", err)
	}

	// Pub/Sub
	if _, err := psclient.InitClient(ctx, projectID, creds); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}

	// Support both JSON string and file path for credentials
	var fsOpts []option.ClientOption
	if strings.HasPrefix(strings.TrimSpace(creds), "{") {
		fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(creds)))
	} else if creds != "" {
		fsOpts = append(fsOpts, option.WithCredentialsFile(creds))
	}
	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("firestore init: %v", err)
	}
	defer fs.Close()

	h := &handler{fs: fs, registry: newViewerRegistry()}

	r := mux.NewRouter()
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := fs.Collection("streams").Limit(1).Documents(healthCtx).Next()
		if err != nil && err != iterator.Done {
			jsonError(w, "health check failed: firestore unreachable", http.StatusServiceUnavailable)
			return
		}

		jsonOK(w, map[string]string{"service": "stream-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()
	v1.HandleFunc("/streams", h.listStreams).Methods(http.MethodGet)
	v1.HandleFunc("/streams/{id}", h.getStream).Methods(http.MethodGet)

	protected := v1.NewRoute().Subrouter()
	protected.Use(middleware.AuthRequired)
	protected.HandleFunc("/streams", h.createStream).Methods(http.MethodPost)
	protected.HandleFunc("/streams/{id}/join", h.joinStream).Methods(http.MethodPost)
	protected.HandleFunc("/streams/{id}/leave", h.leaveStream).Methods(http.MethodPost)
	protected.HandleFunc("/streams/{id}/manifest", h.getManifest).Methods(http.MethodGet)
	protected.HandleFunc("/streams/{id}/reset-viewers", h.resetViewerCount).Methods(http.MethodPost)

	log.Printf("stream-service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
}
