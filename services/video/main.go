package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

// ── Models ────────────────────────────────────────────────────────────────────

type Video struct {
	ID          string    `firestore:"id"          json:"id"`
	Title       string    `firestore:"title"       json:"title"`
	Description string    `firestore:"description" json:"description"`
	Status      string    `firestore:"status"      json:"status"` // uploading | transcoding | ready | failed
	HLSUrl      string    `firestore:"hlsUrl"      json:"hlsUrl"`
	VideoUrl    string    `firestore:"videoUrl"    json:"videoUrl"`
	GCSPath     string    `firestore:"gcsPath"     json:"gcsPath"`
	UploaderUID string    `firestore:"uploaderUid" json:"uploaderUid"`
	StreamID    string    `firestore:"streamId"    json:"streamId,omitempty"`
	ViewCount   int64     `firestore:"viewCount"   json:"viewCount"`
	CreatedAt   time.Time `firestore:"createdAt"   json:"createdAt"`
	UpdatedAt   time.Time `firestore:"updatedAt"   json:"updatedAt"`
}

type UploadRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	StreamID    string `json:"streamId,omitempty"`
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

	video := Video{
		ID: videoID, Title: req.Title, Description: req.Description,
		Status: "uploading", GCSPath: gcsPath, UploaderUID: uid,
		StreamID: req.StreamID, ViewCount: 0, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.fs.Collection("videos").Doc(videoID).Set(r.Context(), video); err != nil {
		jsonError(w, "failed to create video record", http.StatusInternalServerError)
		return
	}

	uploadURL, err := h.createResumableURL(r.Context(), gcsPath)
	if err != nil {
		log.Printf("video: resumable URL failed: %v", err)
		jsonError(w, "failed to generate upload URL", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{
		"videoId":   videoID,
		"uploadUrl": uploadURL,
		"gcsPath":   gcsPath,
	})
}

func (h *handler) createResumableURL(ctx context.Context, gcsPath string) (string, error) {
	authToken, err := getAuthToken(ctx)
	if err != nil {
		return "", err
	}
	encodedPath := strings.NewReplacer("/", "%2F").Replace(gcsPath)
	apiURL := fmt.Sprintf(
		"https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=resumable&name=%s",
		h.bucket, encodedPath,
	)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader("{}"))
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
		return "", fmt.Errorf("no Location header")
	}
	return uploadURL, nil
}

// ── POST /api/v1/videos/{id}/transcode ───────────────────────────────────────
// Uses FFmpeg directly — no external API, no auth issues.

func (h *handler) transcodeVideo(w http.ResponseWriter, r *http.Request) {
	videoID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

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

	role := h.getUserRole(r.Context(), uid)
	if video.UploaderUID != uid && role != "admin" {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	if video.Status == "ready" {
		jsonOK(w, map[string]any{"message": "already ready", "hlsUrl": video.HLSUrl})
		return
	}

	// Update status
	h.fs.Collection("videos").Doc(videoID).Update(r.Context(), []firestore.Update{
		{Path: "status", Value: "transcoding"}, {Path: "updatedAt", Value: time.Now().UTC()},
	})

	// Run FFmpeg in background
	go h.runFFmpeg(videoID, video.GCSPath, video.StreamID)

	jsonOK(w, map[string]any{
		"videoId": videoID,
		"status":  "transcoding",
		"message": "FFmpeg transcoding started — HLS ready in 1-2 minutes",
	})
}

// ── runFFmpeg ─────────────────────────────────────────────────────────────────
// Downloads MP4 from GCS, runs FFmpeg to make HLS, uploads back to GCS.

func (h *handler) runFFmpeg(videoID, gcsPath, streamID string) {
	ctx := context.Background()
	tmpDir := fmt.Sprintf("/tmp/%s", videoID)

	defer os.RemoveAll(tmpDir) // cleanup always

	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		log.Printf("ffmpeg: mkdir failed: %v", err)
		h.markFailed(videoID)
		return
	}

	inputFile := filepath.Join(tmpDir, "input.mp4")
	outputDir := filepath.Join(tmpDir, "hls")
	outputFile := filepath.Join(outputDir, "index.m3u8")

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Printf("ffmpeg: mkdir hls failed: %v", err)
		h.markFailed(videoID)
		return
	}

	// Step 1: Download MP4 from GCS
	log.Printf("ffmpeg: downloading videoId=%s from gs://%s/%s", videoID, h.bucket, gcsPath)
	if err := h.downloadFromGCS(ctx, gcsPath, inputFile); err != nil {
		log.Printf("ffmpeg: download failed videoId=%s: %v", videoID, err)
		h.markFailed(videoID)
		return
	}
	log.Printf("ffmpeg: downloaded videoId=%s", videoID)

	// Step 2: Run FFmpeg — convert to multi-bitrate HLS
	log.Printf("ffmpeg: starting transcoding videoId=%s", videoID)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y",
		"-i", inputFile,
		// 720p stream
		"-vf", "scale=w=1280:h=720:force_original_aspect_ratio=decrease",
		"-c:v", "libx264", "-crf", "23", "-preset", "fast",
		"-c:a", "aac", "-b:a", "128k",
		"-hls_time", "6",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", filepath.Join(outputDir, "segment_%03d.ts"),
		"-f", "hls",
		outputFile,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Printf("ffmpeg: failed videoId=%s: %v", videoID, err)
		h.markFailed(videoID)
		return
	}
	log.Printf("ffmpeg: transcoding complete videoId=%s", videoID)

	// Step 3: Upload HLS files to GCS
	hlsGCSPrefix := fmt.Sprintf("hls/%s", videoID)
	if err := h.uploadDirToGCS(ctx, outputDir, hlsGCSPrefix); err != nil {
		log.Printf("ffmpeg: GCS upload failed videoId=%s: %v", videoID, err)
		h.markFailed(videoID)
		return
	}

	hlsURL := fmt.Sprintf("%s/%s/index.m3u8", h.cdnBase, hlsGCSPrefix)
	log.Printf("ffmpeg: ✅ done videoId=%s hlsUrl=%s", videoID, hlsURL)

	// Step 4: Update Firestore
	h.fs.Collection("videos").Doc(videoID).Update(ctx, []firestore.Update{
		{Path: "status", Value: "ready"},
		{Path: "hlsUrl", Value: hlsURL},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	// Update linked stream if any
	if streamID != "" {
		h.fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
			{Path: "hlsUrl", Value: hlsURL},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
	}

	psclient.PublishEvent(ctx, "stream_events", map[string]any{
		"eventType": "video_ready", "videoId": videoID,
		"hlsUrl": hlsURL, "timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// downloadFromGCS downloads a GCS object to a local file.

func (h *handler) downloadFromGCS(ctx context.Context, gcsPath, localPath string) error {
	obj := h.gcs.Bucket(h.bucket).Object(gcsPath)
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("GCS open: %w", err)
	}
	defer reader.Close()

	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, reader); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}

// uploadDirToGCS uploads all files in localDir to GCS under gcsPrefix.

func (h *handler) uploadDirToGCS(ctx context.Context, localDir, gcsPrefix string) error {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		localFile := filepath.Join(localDir, entry.Name())
		gcsPath := fmt.Sprintf("%s/%s", gcsPrefix, entry.Name())

		if err := h.uploadFileToGCS(ctx, localFile, gcsPath); err != nil {
			return fmt.Errorf("upload %s: %w", entry.Name(), err)
		}
		log.Printf("ffmpeg: uploaded %s", gcsPath)
	}
	return nil
}

func (h *handler) uploadFileToGCS(ctx context.Context, localPath, gcsPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	obj := h.gcs.Bucket(h.bucket).Object(gcsPath)
	w := obj.NewWriter(ctx)

	// Set correct content type
	if strings.HasSuffix(gcsPath, ".m3u8") {
		w.ContentType = "application/vnd.apple.mpegurl"
	} else if strings.HasSuffix(gcsPath, ".ts") {
		w.ContentType = "video/mp2t"
	}
	w.PredefinedACL = "publicRead" // make publicly accessible

	if _, err := io.Copy(w, f); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func (h *handler) markFailed(videoID string) {
	h.fs.Collection("videos").Doc(videoID).Update(context.Background(), []firestore.Update{
		{Path: "status", Value: "failed"}, {Path: "updatedAt", Value: time.Now().UTC()},
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
		jsonError(w, fmt.Sprintf("not ready — status: %s", v.Status), http.StatusAccepted)
		return
	}
	jsonOK(w, map[string]string{"videoId": videoID, "manifestUrl": v.HLSUrl, "status": v.Status})
}

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
	go func() {
		ctx := context.Background()
		h.gcs.Bucket(h.bucket).Object(v.GCSPath).Delete(ctx)
		it := h.gcs.Bucket(h.bucket).Objects(ctx, &storage.Query{Prefix: fmt.Sprintf("hls/%s/", videoID)})
		for {
			attrs, err := it.Next()
			if err != nil {
				break
			}
			h.gcs.Bucket(h.bucket).Object(attrs.Name).Delete(ctx)
		}
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
		log.Fatalf("video: firestore: %v", err)
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
		log.Fatalf("video: gcs: %v", err)
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
	v1.HandleFunc("/videos/{id}/manifest", h.getManifest).Methods(http.MethodGet)

	protected := v1.NewRoute().Subrouter()
	protected.Use(middleware.AuthRequired)
	protected.HandleFunc("/videos/upload-url", h.getUploadURL).Methods(http.MethodPost)
	protected.HandleFunc("/videos/{id}/transcode", h.transcodeVideo).Methods(http.MethodPost)
	protected.HandleFunc("/videos/{id}", h.deleteVideo).Methods(http.MethodDelete)

	log.Printf("video-service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("video: %v", err)
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
