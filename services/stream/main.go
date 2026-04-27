package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

// ── Models ────────────────────────────────────────────────────────────────────

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

// ── Handler ───────────────────────────────────────────────────────────────────
// ViewerRegistry is REMOVED.
// Viewer join/leave idempotency is handled via Firestore subcollection
// streams/{streamId}/viewers/{uid} — correct across ALL Cloud Run instances.

type handler struct {
	fs *firestore.Client
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

// ── createStream ──────────────────────────────────────────────────────────────

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
		ID:             streamID,
		Title:          req.Title,
		Status:         "live",
		ViewerCount:    0,
		BroadcasterUID: uid,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := h.fs.Collection("streams").Doc(streamID).Set(r.Context(), stream); err != nil {
		jsonError(w, "failed to create stream", http.StatusInternalServerError)
		return
	}
	go startTranscoder(streamID, req.RTMPUrl, h.fs)
	go psclient.PublishEvent(context.Background(), "stream_events", map[string]any{
		"eventId":   newEventID("stream_started"),
		"eventType": "stream_started",
		"streamId":  streamID,
		"title":     req.Title,
		"uid":       uid,
		"timestamp": now.Format(time.RFC3339),
	})
	jsonOK(w, stream)
}

// ── listStreams ───────────────────────────────────────────────────────────────

func (h *handler) listStreams(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("streams").
		Where("status", "==", "live").
		Limit(50).
		Documents(r.Context()).GetAll()
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

// ── getStream ─────────────────────────────────────────────────────────────────

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

// ── joinStream ────────────────────────────────────────────────────────────────
// Uses Firestore subcollection streams/{id}/viewers/{uid} for idempotency.
// FIX 1: Checks stream status — cannot join an ended stream.
// FIX 2: Transaction is safe across all Cloud Run instances.
// FIX 3: Joining twice returns current count without double-incrementing.

func (h *handler) joinStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	streamRef := h.fs.Collection("streams").Doc(streamID)
	viewerRef := streamRef.Collection("viewers").Doc(uid)

	var newCount int
	err := h.fs.RunTransaction(r.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
		// Read stream doc first — check it exists and is still live
		streamSnap, err := tx.Get(streamRef)
		if err != nil {
			return err
		}
		var s Stream
		streamSnap.DataTo(&s)

		// FIX 1: Reject join if stream has ended
		if s.Status == "ended" {
			return fmt.Errorf("stream has ended")
		}

		// Check if viewer already joined
		viewerSnap, _ := tx.Get(viewerRef)
		if viewerSnap.Exists() {
			// Already joined — idempotent, return current count unchanged
			newCount = s.ViewerCount
			return nil
		}

		// New viewer — write presence doc and increment count
		newCount = s.ViewerCount + 1
		tx.Set(viewerRef, map[string]any{
			"uid":      uid,
			"joinedAt": time.Now().UTC(),
		})
		return tx.Update(streamRef, []firestore.Update{
			{Path: "viewerCount", Value: newCount},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
	})
	if err != nil {
		if err.Error() == "stream has ended" {
			jsonError(w, "stream has ended", http.StatusGone)
			return
		}
		jsonError(w, "failed to join stream", http.StatusInternalServerError)
		return
	}

	// Pub/Sub — for analytics only (currentViewers, peakViewers, totalJoins)
	go psclient.PublishEvent(context.Background(), "viewer_events", map[string]any{
		"eventId":   newEventID("viewer_join"),
		"eventType": "viewer_join",
		"streamId":  streamID,
		"uid":       uid,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	jsonOK(w, map[string]any{"joined": true, "viewerCount": newCount})
}

// ── leaveStream ───────────────────────────────────────────────────────────────
// Uses Firestore subcollection to check presence before decrementing.
// FIX: Safe across all Cloud Run instances. Leaving twice has no effect.
// Floor of 0 — viewerCount can never go negative.

func (h *handler) leaveStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	streamRef := h.fs.Collection("streams").Doc(streamID)
	viewerRef := streamRef.Collection("viewers").Doc(uid)

	var newCount int
	err := h.fs.RunTransaction(r.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
		// Check if viewer is actually present
		viewerSnap, _ := tx.Get(viewerRef)
		if !viewerSnap.Exists() {
			// Not in stream — idempotent, nothing to do
			return nil
		}

		// Read current stream count
		streamSnap, err := tx.Get(streamRef)
		if err != nil {
			return err
		}
		var s Stream
		streamSnap.DataTo(&s)

		newCount = s.ViewerCount - 1
		if newCount < 0 {
			newCount = 0 // floor at 0 — never go negative
		}

		// Delete viewer presence doc
		tx.Delete(viewerRef)

		// Decrement viewerCount
		return tx.Update(streamRef, []firestore.Update{
			{Path: "viewerCount", Value: newCount},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
	})
	if err != nil {
		log.Printf("leaveStream transaction error streamId=%s: %v", streamID, err)
		jsonError(w, "failed to leave stream", http.StatusInternalServerError)
		return
	}

	// Pub/Sub — for analytics only
	go psclient.PublishEvent(context.Background(), "viewer_events", map[string]any{
		"eventId":   newEventID("viewer_leave"),
		"eventType": "viewer_leave",
		"streamId":  streamID,
		"uid":       uid,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	jsonOK(w, map[string]any{"left": true, "viewerCount": newCount})
}

// ── getManifest ───────────────────────────────────────────────────────────────

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

// ── resetViewerCount ──────────────────────────────────────────────────────────
// Admin only. Resets viewerCount to 0 and batch-deletes all viewer presence docs.

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

	// FIX: Batch-delete all viewer presence docs (max 500 per batch)
	go func() {
		ctx := context.Background()
		docs, err := h.fs.Collection("streams").Doc(streamID).
			Collection("viewers").Documents(ctx).GetAll()
		if err != nil {
			log.Printf("resetViewerCount: failed to fetch viewers: %v", err)
			return
		}
		if len(docs) == 0 {
			return
		}
		batch := h.fs.Batch()
		for i, d := range docs {
			batch.Delete(d.Ref)
			if (i+1)%500 == 0 {
				if _, err := batch.Commit(ctx); err != nil {
					log.Printf("resetViewerCount: batch commit error: %v", err)
				}
				batch = h.fs.Batch()
			}
		}
		if len(docs)%500 != 0 {
			if _, err := batch.Commit(ctx); err != nil {
				log.Printf("resetViewerCount: final batch commit error: %v", err)
			}
		}
		log.Printf("resetViewerCount: cleared %d viewer docs for streamId=%s", len(docs), streamID)
	}()

	jsonOK(w, map[string]any{"reset": true, "viewerCount": 0})
}

// ── Cloud Transcoder API ──────────────────────────────────────────────────────
// Replaces self-managed FFmpeg. Fully managed GCP encoding.
// FIX 1: Captures job name from submit response — polls specific job URL.
// FIX 2: Clears viewer subcollection when stream ends.

func startTranscoder(streamID, inputURI string, fs *firestore.Client) {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	gcsBucket := util.Getenv("GCS_BUCKET", "sports-stream-66553.appspot.com")
	cdnBase := util.Getenv("CDN_BASE_URL", "https://storage.googleapis.com/"+gcsBucket)
	location := util.Getenv("TRANSCODER_LOCATION", "europe-west1")

	outputURI := fmt.Sprintf("gs://%s/hls/%s/", gcsBucket, streamID)
	hlsURL := fmt.Sprintf("%s/hls/%s/index.m3u8", cdnBase, streamID)
	parent := fmt.Sprintf("projects/%s/locations/%s", projectID, location)
	submitURL := fmt.Sprintf("https://transcoder.googleapis.com/v1/%s/jobs", parent)

	jobPayload := map[string]any{
		"inputUri":  inputURI,
		"outputUri": outputURI,
		"elementaryStreams": []map[string]any{
			{
				"key": "video-720p",
				"videoStream": map[string]any{
					"h264": map[string]any{
						"heightPixels": 720, "widthPixels": 1280,
						"bitrateBps": 1500000, "frameRate": 30,
					},
				},
			},
			{
				"key": "video-480p",
				"videoStream": map[string]any{
					"h264": map[string]any{
						"heightPixels": 480, "widthPixels": 854,
						"bitrateBps": 800000, "frameRate": 30,
					},
				},
			},
			{
				"key": "video-360p",
				"videoStream": map[string]any{
					"h264": map[string]any{
						"heightPixels": 360, "widthPixels": 640,
						"bitrateBps": 400000, "frameRate": 30,
					},
				},
			},
			{
				"key": "audio",
				"audioStream": map[string]any{
					"codec": "aac", "bitrateBps": 128000,
				},
			},
		},
		"muxStreams": []map[string]any{
			{
				"key": "hls-720p", "container": "ts",
				"elementaryStreams": []string{"video-720p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "2s"},
			},
			{
				"key": "hls-480p", "container": "ts",
				"elementaryStreams": []string{"video-480p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "2s"},
			},
			{
				"key": "hls-360p", "container": "ts",
				"elementaryStreams": []string{"video-360p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "2s"},
			},
		},
		"manifests": []map[string]any{
			{
				"fileName":   "index.m3u8",
				"type":       "HLS",
				"muxStreams": []string{"hls-720p", "hls-480p", "hls-360p"},
			},
		},
	}

	body, err := json.Marshal(map[string]any{"job": jobPayload})
	if err != nil {
		log.Printf("transcoder: marshal failed streamId=%s: %v", streamID, err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("transcoder: request build failed streamId=%s: %v", streamID, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("transcoder: API call failed streamId=%s: %v", streamID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("transcoder: API returned %d for streamId=%s", resp.StatusCode, streamID)
		return
	}

	// FIX 1: Capture job name from submit response so we poll the correct URL
	var jobResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
		log.Printf("transcoder: failed to decode job response streamId=%s: %v", streamID, err)
		return
	}
	jobName, _ := jobResp["name"].(string) // e.g. "projects/xxx/locations/yyy/jobs/zzz"
	if jobName == "" {
		log.Printf("transcoder: no job name in response streamId=%s", streamID)
		return
	}
	// Poll this specific job URL — not the list URL
	jobURL := fmt.Sprintf("https://transcoder.googleapis.com/v1/%s", jobName)

	log.Printf("transcoder: job submitted streamId=%s job=%s", streamID, jobName)

	// Write HLS URL to Firestore immediately — ExoPlayer retries until segments ready
	fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
		{Path: "hlsUrl", Value: hlsURL},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	// Poll specific job URL for completion
	go func() {
		for i := 0; i < 120; i++ { // every 5s, up to 10 minutes
			time.Sleep(5 * time.Second)

			checkReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, jobURL, nil)
			checkResp, err := httpClient.Do(checkReq)
			if err != nil {
				log.Printf("transcoder: poll error streamId=%s: %v", streamID, err)
				continue
			}
			var result map[string]any
			json.NewDecoder(checkResp.Body).Decode(&result)
			checkResp.Body.Close()

			state, _ := result["state"].(string)
			log.Printf("transcoder: job state=%s streamId=%s", state, streamID)

			if state == "SUCCEEDED" || state == "FAILED" {
				break
			}
		}

		// Mark stream as ended
		fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
			{Path: "status", Value: "ended"},
			{Path: "viewerCount", Value: 0},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})

		// FIX 2: Batch-delete viewer subcollection when stream ends
		viewerDocs, err := fs.Collection("streams").Doc(streamID).
			Collection("viewers").Documents(ctx).GetAll()
		if err == nil && len(viewerDocs) > 0 {
			batch := fs.Batch()
			for i, d := range viewerDocs {
				batch.Delete(d.Ref)
				if (i+1)%500 == 0 {
					batch.Commit(ctx)
					batch = fs.Batch()
				}
			}
			if len(viewerDocs)%500 != 0 {
				batch.Commit(ctx)
			}
			log.Printf("transcoder: cleared %d viewer docs for streamId=%s", len(viewerDocs), streamID)
		}

		// Publish stream_ended event
		psclient.PublishEvent(ctx, "stream_events", map[string]any{
			"eventId":   newEventID("stream_ended"),
			"eventType": "stream_ended",
			"streamId":  streamID,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	}()
}

func newEventID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UTC().UnixNano())
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.ProjectID()
	port := util.Getenv("PORT", "8082")
	creds := util.Getenv("FIREBASE_CREDENTIALS", "")

	if _, err := fbclient.InitClient(ctx, creds); err != nil {
		log.Fatalf("firebase init: %v", err)
	}
	if _, err := psclient.InitClient(ctx, projectID, creds); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}

	var fsOpts []option.ClientOption
	if util.LooksLikeJSONCredential(creds) {
		fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(creds)))
	} else if util.FileExists(creds) {
		fsOpts = append(fsOpts, option.WithCredentialsFile(creds))
	} else if creds != "" {
		log.Printf("stream-service: credential file %q not found; falling back to default credentials", creds)
	}
	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("firestore init: %v", err)
	}
	defer fs.Close()

	h := &handler{fs: fs}

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

// ── Helpers ───────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
}
