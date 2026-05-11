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
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fbclient "sports-stream-backend/pkg/firebase"
	"sports-stream-backend/pkg/middleware"
	"sports-stream-backend/pkg/util"
)

type Video struct {
	ID          string    `firestore:"id"          json:"id"`
	Title       string    `firestore:"title"       json:"title"`
	Description string    `firestore:"description" json:"description"`
	VideoUrl    string    `firestore:"videoUrl"    json:"videoUrl"`
	GCSPath     string    `firestore:"gcsPath"     json:"gcsPath"`
	UploaderUID string    `firestore:"uploaderUid" json:"uploaderUid"`
	StreamID    string    `firestore:"streamId"    json:"streamId,omitempty"`
	ViewCount   int64     `firestore:"viewCount"   json:"viewCount"`
	Size        int64     `firestore:"size"        json:"size"`
	CreatedAt   time.Time `firestore:"createdAt"   json:"createdAt"`
	UpdatedAt   time.Time `firestore:"updatedAt"   json:"updatedAt"`
}

type UploadRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	StreamID    string `json:"streamId,omitempty"`
}

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
	var p struct {
		Role string `firestore:"role"`
	}
	snap.DataTo(&p)
	return p.Role
}

func getAuthToken(ctx context.Context) (string, error) {
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", err
	}
	tok, err := ts.Token()
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// ── POST /api/v1/videos/upload-url ───────────────────────────────────────────
// Returns a resumable GCS upload URL. Browser PUT video directly to GCS.
// No transcoding — video plays directly from GCS URL.

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
	gcsPath := fmt.Sprintf("videos/%s/%s.mp4", uid, videoID)

	// Public URL — directly playable by ExoPlayer
	videoURL := fmt.Sprintf("%s/%s", h.cdnBase, gcsPath)

	// Save to Firestore immediately
	video := Video{
		ID: videoID, Title: req.Title, Description: req.Description,
		VideoUrl: videoURL, GCSPath: gcsPath, UploaderUID: uid,
		StreamID: req.StreamID, ViewCount: 0, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.fs.Collection("videos").Doc(videoID).Set(r.Context(), video); err != nil {
		jsonError(w, "failed to create video record", http.StatusInternalServerError)
		return
	}

	// Get resumable upload URL
	uploadURL, err := h.createResumableURL(r.Context(), gcsPath)
	if err != nil {
		log.Printf("video: resumable URL failed: %v", err)
		jsonError(w, "failed to generate upload URL", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{
		"videoId":   videoID,
		"uploadUrl": uploadURL,
		"videoUrl":  videoURL, // ← direct playable URL, use this in ExoPlayer
		"gcsPath":   gcsPath,
	})
}

// createResumableURL creates a GCS resumable upload session URL.

func (h *handler) createResumableURL(ctx context.Context, gcsPath string) (string, error) {
	authToken, err := getAuthToken(ctx)
	if err != nil {
		return "", err
	}

	encodedPath := strings.NewReplacer("/", "%2F").Replace(gcsPath)
	apiURL := fmt.Sprintf(
		"https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=resumable&name=%s&predefinedAcl=publicRead",
		h.bucket, encodedPath,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL,
		strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upload-Content-Type", "video/mp4")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GCS %d: %s", resp.StatusCode, string(body))
	}

	uploadURL := resp.Header.Get("Location")
	if uploadURL == "" {
		return "", fmt.Errorf("no Location header in GCS response")
	}
	return uploadURL, nil
}

// ── POST /api/v1/videos/{id}/confirm ─────────────────────────────────────────
// Call after upload completes to confirm video is ready.

func (h *handler) confirmUpload(w http.ResponseWriter, r *http.Request) {
	videoID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	snap, err := h.fs.Collection("videos").Doc(videoID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "video not found", http.StatusNotFound)
		return
	}
	var v Video
	snap.DataTo(&v)

	if v.UploaderUID != uid && h.getUserRole(r.Context(), uid) != "admin" {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}

	// Verify file actually exists in GCS
	authToken, err := getAuthToken(r.Context())
	if err != nil {
		jsonError(w, "auth error", http.StatusInternalServerError)
		return
	}

	encodedPath := strings.NewReplacer("/", "%2F").Replace(v.GCSPath)
	checkURL := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s",
		h.bucket, encodedPath)
	checkReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, checkURL, nil)
	checkReq.Header.Set("Authorization", "Bearer "+authToken)
	checkResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(checkReq)
	if err != nil || checkResp.StatusCode == 404 {
		jsonError(w, "file not found in GCS — upload may have failed", http.StatusBadRequest)
		return
	}

	// Get file size from GCS metadata
	var meta map[string]any
	json.NewDecoder(checkResp.Body).Decode(&meta)
	checkResp.Body.Close()

	sizeStr, _ := meta["size"].(string)
	var size int64
	fmt.Sscanf(sizeStr, "%d", &size)

	// Update Firestore
	h.fs.Collection("videos").Doc(videoID).Update(r.Context(), []firestore.Update{
		{Path: "size", Value: size},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	jsonOK(w, map[string]any{
		"videoId":  videoID,
		"videoUrl": v.VideoUrl,
		"size":     size,
		"ready":    true,
	})
}

// ── GET /api/v1/videos ────────────────────────────────────────────────────────

func (h *handler) listVideos(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("videos").
		OrderBy("createdAt", firestore.Desc).
		Limit(50).Documents(r.Context()).GetAll()
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
	go h.fs.Collection("videos").Doc(videoID).Update(context.Background(), []firestore.Update{
		{Path: "viewCount", Value: firestore.Increment(1)},
	})
	jsonOK(w, v)
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
	if v.UploaderUID != uid && h.getUserRole(r.Context(), uid) != "admin" {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	h.fs.Collection("videos").Doc(videoID).Delete(r.Context())
	go h.gcs.Bucket(h.bucket).Object(v.GCSPath).Delete(context.Background())
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

	h := &handler{fs: fs, gcs: gcs, bucket: bucket, cdnBase: cdnBase}

	r := mux.NewRouter()
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, map[string]string{"service": "video-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()
	v1.HandleFunc("/videos", h.listVideos).Methods(http.MethodGet)
	v1.HandleFunc("/videos/{id}", h.getVideo).Methods(http.MethodGet)

	protected := v1.NewRoute().Subrouter()
	protected.Use(middleware.AuthRequired)
	protected.HandleFunc("/videos/upload-url", h.getUploadURL).Methods(http.MethodPost)
	protected.HandleFunc("/videos/{id}/confirm", h.confirmUpload).Methods(http.MethodPost)
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
