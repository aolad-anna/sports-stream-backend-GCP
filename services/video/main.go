package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/gorilla/mux"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fbclient "sports-stream-backend/pkg/firebase"
	"sports-stream-backend/pkg/middleware"
	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

// ── Models ────────────────────────────────────────────────────────────────────

type Video struct {
	ID           string    `firestore:"id"             json:"id"`
	Title        string    `firestore:"title"          json:"title"`
	Description  string    `firestore:"description"    json:"description"`
	Status       string    `firestore:"status"         json:"status"` // uploading | transcoding | ready | failed
	HLSUrl       string    `firestore:"hlsUrl"         json:"hlsUrl"`
	ThumbnailUrl string    `firestore:"thumbnailUrl"   json:"thumbnailUrl"`
	RawGCSPath   string    `firestore:"rawGcsPath"     json:"rawGcsPath"`
	DurationSecs int       `firestore:"durationSecs"   json:"durationSecs"`
	UploaderUID  string    `firestore:"uploaderUid"    json:"uploaderUid"`
	StreamID     string    `firestore:"streamId"       json:"streamId,omitempty"`
	ViewCount    int64     `firestore:"viewCount"      json:"viewCount"`
	CreatedAt    time.Time `firestore:"createdAt"      json:"createdAt"`
	UpdatedAt    time.Time `firestore:"updatedAt"      json:"updatedAt"`
}

type UploadRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	StreamID    string `json:"streamId,omitempty"` // optional — link to a stream
}

type TranscodeRequest struct {
	VideoID string `json:"videoId"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

type handler struct {
	fs      *firestore.Client
	gcs     *storage.Client
	bucket  string
	cdnBase string
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

// ── POST /api/v1/videos/upload-url ────────────────────────────────────────────
// Returns a signed GCS upload URL so Android can upload directly to GCS.
// No video data passes through the backend — secure and efficient.

func (h *handler) getUploadURL(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())
	role := h.getUserRole(r.Context(), uid)
	if role != "broadcaster" && role != "admin" {
		jsonError(w, "only broadcasters can upload videos", http.StatusForbidden)
		return
	}

	var req UploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
		jsonError(w, "title is required", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	videoID := fmt.Sprintf("video_%d", now.UnixMilli())
	gcsPath := fmt.Sprintf("uploads/%s/%s.mp4", uid, videoID)

	// Create video doc in Firestore with status=uploading
	video := Video{
		ID:          videoID,
		Title:       req.Title,
		Description: req.Description,
		Status:      "uploading",
		RawGCSPath:  gcsPath,
		UploaderUID: uid,
		StreamID:    req.StreamID,
		ViewCount:   0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if _, err := h.fs.Collection("videos").Doc(videoID).Set(r.Context(), video); err != nil {
		jsonError(w, "failed to create video record", http.StatusInternalServerError)
		return
	}

	// Generate signed upload URL — valid for 15 minutes
	opts := &storage.SignedURLOptions{
		Method:      "PUT",
		Expires:     time.Now().Add(15 * time.Minute),
		ContentType: "video/mp4",
	}
	signedURL, err := h.gcs.Bucket(h.bucket).SignedURL(gcsPath, opts)
	if err != nil {
		log.Printf("video: failed to generate signed URL: %v", err)
		// Return unsigned URL for local development
		signedURL = fmt.Sprintf("https://storage.googleapis.com/%s/%s", h.bucket, gcsPath)
	}

	jsonOK(w, map[string]any{
		"videoId":   videoID,
		"uploadUrl": signedURL,
		"gcsPath":   gcsPath,
		"expiresIn": "15m",
	})
}

// ── POST /api/v1/videos/{id}/transcode ────────────────────────────────────────
// Called after Android finishes uploading. Triggers Cloud Transcoder API.

func (h *handler) transcodeVideo(w http.ResponseWriter, r *http.Request) {
	videoID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	// Get video doc
	snap, err := h.fs.Collection("videos").Doc(videoID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "video not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "firestore error", http.StatusInternalServerError)
		return
	}
	var video Video
	snap.DataTo(&video)

	// Only uploader or admin can trigger transcode
	role := h.getUserRole(r.Context(), uid)
	if video.UploaderUID != uid && role != "admin" {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}

	if video.Status == "ready" {
		jsonOK(w, map[string]any{"message": "already transcoded", "hlsUrl": video.HLSUrl})
		return
	}

	// Update status to transcoding
	h.fs.Collection("videos").Doc(videoID).Update(r.Context(), []firestore.Update{
		{Path: "status", Value: "transcoding"},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	// Start transcoding in background
	go h.runTranscoder(videoID, video.RawGCSPath)

	jsonOK(w, map[string]any{
		"videoId": videoID,
		"status":  "transcoding",
		"message": "transcoding started — hlsUrl will be available in 1-3 minutes",
	})
}

// ── runTranscoder — calls GCP Cloud Transcoder API ───────────────────────────

func (h *handler) runTranscoder(videoID, inputGCSPath string) {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	location := util.Getenv("TRANSCODER_LOCATION", "europe-west1")

	inputURI := fmt.Sprintf("gs://%s/%s", h.bucket, inputGCSPath)
	outputURI := fmt.Sprintf("gs://%s/hls/%s/", h.bucket, videoID)
	hlsURL := fmt.Sprintf("%s/hls/%s/index.m3u8", h.cdnBase, videoID)
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
				"segmentSettings":   map[string]any{"segmentDuration": "6s"},
			},
			{
				"key": "hls-480p", "container": "ts",
				"elementaryStreams": []string{"video-480p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "6s"},
			},
			{
				"key": "hls-360p", "container": "ts",
				"elementaryStreams": []string{"video-360p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "6s"},
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

	body, _ := json.Marshal(map[string]any{"job": jobPayload})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, strings.NewReader(string(body)))
	if err != nil {
		log.Printf("video: transcode request build failed videoId=%s: %v", videoID, err)
		h.markFailed(videoID)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("video: transcode API call failed videoId=%s: %v", videoID, err)
		h.markFailed(videoID)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("video: transcode API error %d videoId=%s: %s", resp.StatusCode, videoID, body)
		h.markFailed(videoID)
		return
	}

	// Capture job name to poll specific job
	var jobResp map[string]any
	json.NewDecoder(resp.Body).Decode(&jobResp)
	jobName, _ := jobResp["name"].(string)
	if jobName == "" {
		log.Printf("video: no job name in response videoId=%s", videoID)
		h.markFailed(videoID)
		return
	}

	jobURL := fmt.Sprintf("https://transcoder.googleapis.com/v1/%s", jobName)
	log.Printf("video: transcoding job started videoId=%s job=%s", videoID, jobName)

	// Poll job status every 10s up to 15 minutes
	for i := 0; i < 90; i++ {
		time.Sleep(10 * time.Second)

		checkReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, jobURL, nil)
		checkResp, err := client.Do(checkReq)
		if err != nil {
			continue
		}
		var result map[string]any
		json.NewDecoder(checkResp.Body).Decode(&result)
		checkResp.Body.Close()

		state, _ := result["state"].(string)
		log.Printf("video: job state=%s videoId=%s", state, videoID)

		if state == "SUCCEEDED" {
			// Update Firestore with ready status and HLS URL
			h.fs.Collection("videos").Doc(videoID).Update(ctx, []firestore.Update{
				{Path: "status", Value: "ready"},
				{Path: "hlsUrl", Value: hlsURL},
				{Path: "updatedAt", Value: time.Now().UTC()},
			})
			log.Printf("video: ✅ transcoding complete videoId=%s hlsUrl=%s", videoID, hlsURL)

			// If linked to a stream, update stream's hlsUrl too
			snap, _ := h.fs.Collection("videos").Doc(videoID).Get(ctx)
			var v Video
			if snap != nil {
				snap.DataTo(&v)
				if v.StreamID != "" {
					h.fs.Collection("streams").Doc(v.StreamID).Update(ctx, []firestore.Update{
						{Path: "hlsUrl", Value: hlsURL},
						{Path: "updatedAt", Value: time.Now().UTC()},
					})
				}
			}

			// Publish event
			psclient.PublishEvent(ctx, "stream_events", map[string]any{
				"eventType": "video_ready",
				"videoId":   videoID,
				"hlsUrl":    hlsURL,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			})
			return

		} else if state == "FAILED" {
			log.Printf("video: ❌ transcoding failed videoId=%s", videoID)
			h.markFailed(videoID)
			return
		}
	}

	// Timeout
	log.Printf("video: transcoding timed out videoId=%s", videoID)
	h.markFailed(videoID)
}

func (h *handler) markFailed(videoID string) {
	h.fs.Collection("videos").Doc(videoID).Update(context.Background(), []firestore.Update{
		{Path: "status", Value: "failed"},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})
}

// ── GET /api/v1/videos ────────────────────────────────────────────────────────
// List all ready videos

func (h *handler) listVideos(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("videos").
		Where("status", "==", "ready").
		OrderBy("createdAt", firestore.Desc).
		Limit(50).
		Documents(r.Context()).GetAll()
	if err != nil {
		jsonError(w, "failed to fetch videos", http.StatusInternalServerError)
		return
	}
	videos := make([]Video, 0, len(docs))
	for _, d := range docs {
		var v Video
		if err := d.DataTo(&v); err == nil {
			videos = append(videos, v)
		}
	}
	jsonOK(w, videos)
}

// ── GET /api/v1/videos/{id} ───────────────────────────────────────────────────

func (h *handler) getVideo(w http.ResponseWriter, r *http.Request) {
	videoID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("videos").Doc(videoID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "video not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "firestore error", http.StatusInternalServerError)
		return
	}
	var v Video
	snap.DataTo(&v)

	// Increment view count
	go h.fs.Collection("videos").Doc(videoID).Update(context.Background(), []firestore.Update{
		{Path: "viewCount", Value: firestore.Increment(1)},
	})

	jsonOK(w, v)
}

// ── GET /api/v1/videos/{id}/manifest ─────────────────────────────────────────
// Returns HLS URL for ExoPlayer

func (h *handler) getManifest(w http.ResponseWriter, r *http.Request) {
	videoID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("videos").Doc(videoID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "video not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "firestore error", http.StatusInternalServerError)
		return
	}
	var v Video
	snap.DataTo(&v)

	if v.Status != "ready" || v.HLSUrl == "" {
		jsonError(w, fmt.Sprintf("video not ready — status: %s", v.Status), http.StatusAccepted)
		return
	}

	jsonOK(w, map[string]string{
		"videoId":     videoID,
		"manifestUrl": v.HLSUrl,
		"status":      v.Status,
	})
}

// ── DELETE /api/v1/videos/{id} ────────────────────────────────────────────────

func (h *handler) deleteVideo(w http.ResponseWriter, r *http.Request) {
	videoID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	snap, err := h.fs.Collection("videos").Doc(videoID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "video not found", http.StatusNotFound)
		return
	}
	var v Video
	snap.DataTo(&v)

	role := h.getUserRole(r.Context(), uid)
	if v.UploaderUID != uid && role != "admin" {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}

	// Delete Firestore doc
	h.fs.Collection("videos").Doc(videoID).Delete(r.Context())

	// Delete GCS files in background
	go func() {
		ctx := context.Background()
		// Delete raw upload
		h.gcs.Bucket(h.bucket).Object(v.RawGCSPath).Delete(ctx)
		// Delete HLS segments
		prefix := fmt.Sprintf("hls/%s/", videoID)
		it := h.gcs.Bucket(h.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
		for {
			attrs, err := it.Next()
			if err != nil {
				break
			}
			h.gcs.Bucket(h.bucket).Object(attrs.Name).Delete(ctx)
		}
		log.Printf("video: deleted all GCS files for videoId=%s", videoID)
	}()

	jsonOK(w, map[string]any{"deleted": true, "videoId": videoID})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8086")
	creds := util.Getenv("FIREBASE_CREDENTIALS", "")
	bucket := util.Getenv("GCS_BUCKET", "sports-stream-66553.appspot.com")
	cdnBase := util.Getenv("CDN_BASE_URL", "https://storage.googleapis.com/"+bucket)

	if _, err := fbclient.InitClient(ctx, creds); err != nil {
		log.Fatalf("video: firebase init: %v", err)
	}
	if _, err := psclient.InitClient(ctx, projectID, creds); err != nil {
		log.Fatalf("video: pubsub init: %v", err)
	}

	var fsOpts []option.ClientOption
	if strings.HasPrefix(strings.TrimSpace(creds), "{") {
		fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(creds)))
	} else if creds != "" {
		fsOpts = append(fsOpts, option.WithCredentialsFile(creds))
	}

	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("video: firestore init: %v", err)
	}
	defer fs.Close()

	var gcsOpts []option.ClientOption
	if strings.HasPrefix(strings.TrimSpace(creds), "{") {
		gcsOpts = append(gcsOpts, option.WithCredentialsJSON([]byte(creds)))
	} else if creds != "" {
		gcsOpts = append(gcsOpts, option.WithCredentialsFile(creds))
	}
	gcs, err := storage.NewClient(ctx, gcsOpts...)
	if err != nil {
		log.Fatalf("video: gcs init: %v", err)
	}
	defer gcs.Close()

	h := &handler{
		fs:      fs,
		gcs:     gcs,
		bucket:  bucket,
		cdnBase: cdnBase,
	}

	r := mux.NewRouter()
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, map[string]string{"service": "video-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()

	// Public
	v1.HandleFunc("/videos", h.listVideos).Methods(http.MethodGet)
	v1.HandleFunc("/videos/{id}", h.getVideo).Methods(http.MethodGet)
	v1.HandleFunc("/videos/{id}/manifest", h.getManifest).Methods(http.MethodGet)

	// Protected
	protected := v1.NewRoute().Subrouter()
	protected.Use(middleware.AuthRequired)
	protected.HandleFunc("/videos/upload-url", h.getUploadURL).Methods(http.MethodPost)
	protected.HandleFunc("/videos/{id}/transcode", h.transcodeVideo).Methods(http.MethodPost)
	protected.HandleFunc("/videos/{id}", h.deleteVideo).Methods(http.MethodDelete)

	log.Printf("video-service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("video: ListenAndServe: %v", err)
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
