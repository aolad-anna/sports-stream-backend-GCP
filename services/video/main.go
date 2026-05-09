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
	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

// ── Models ────────────────────────────────────────────────────────────────────

type Video struct {
	ID           string    `firestore:"id"             json:"id"`
	Title        string    `firestore:"title"          json:"title"`
	Description  string    `firestore:"description"    json:"description"`
	Status       string    `firestore:"status"         json:"status"`
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
	var profile struct {
		Role string `firestore:"role"`
	}
	snap.DataTo(&profile)
	return profile.Role
}

// ── getAuthToken ──────────────────────────────────────────────────────────────
// Uses Application Default Credentials — automatic on Cloud Run.

func getAuthToken(ctx context.Context) (string, error) {
	ts, err := google.DefaultTokenSource(ctx,
		"https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", fmt.Errorf("token source: %w", err)
	}
	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("get token: %w", err)
	}
	return tok.AccessToken, nil
}

// ── POST /api/v1/videos/upload-url ───────────────────────────────────────────
// FIX: Instead of signed URL (which requires complex IAM setup),
// we generate a resumable upload URL using the GCS JSON API with
// our service account token. This works reliably on Cloud Run.

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
		Status: "uploading", RawGCSPath: gcsPath, UploaderUID: uid,
		StreamID: req.StreamID, ViewCount: 0, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.fs.Collection("videos").Doc(videoID).Set(r.Context(), video); err != nil {
		jsonError(w, "failed to create video record", http.StatusInternalServerError)
		return
	}

	// FIX: Use GCS resumable upload URL via JSON API
	// This requires only an OAuth2 token — no signing needed
	uploadURL, err := h.createResumableUploadURL(r.Context(), gcsPath)
	if err != nil {
		log.Printf("video: resumable upload URL failed: %v", err)
		jsonError(w, "failed to generate upload URL", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{
		"videoId":   videoID,
		"uploadUrl": uploadURL,
		"gcsPath":   gcsPath,
		"expiresIn": "15m",
	})
}

// ── createResumableUploadURL ──────────────────────────────────────────────────
// Creates a GCS resumable upload session URL using OAuth2 token.
// No signing required — works on Cloud Run with any service account.

func (h *handler) createResumableUploadURL(ctx context.Context, gcsPath string) (string, error) {
	authToken, err := getAuthToken(ctx)
	if err != nil {
		return "", fmt.Errorf("auth token: %w", err)
	}

	// GCS JSON API resumable upload initiation
	// FIX: URL-encode the object name (slashes must be %2F)
	// Bucket name with dots works fine in path — no encoding needed
	encodedPath := strings.NewReplacer("/", "%2F").Replace(gcsPath)
	apiURL := fmt.Sprintf(
		"https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=resumable&name=%s",
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
		return "", fmt.Errorf("initiate upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GCS returned %d: %s", resp.StatusCode, string(body))
	}

	// The resumable upload URL is in the Location header
	uploadURL := resp.Header.Get("Location")
	if uploadURL == "" {
		return "", fmt.Errorf("no Location header in GCS response")
	}

	log.Printf("video: resumable upload URL created for %s", gcsPath)
	return uploadURL, nil
}

// ── POST /api/v1/videos/{id}/transcode ───────────────────────────────────────

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
		jsonOK(w, map[string]any{"message": "already transcoded", "hlsUrl": video.HLSUrl})
		return
	}

	h.fs.Collection("videos").Doc(videoID).Update(r.Context(), []firestore.Update{
		{Path: "status", Value: "transcoding"}, {Path: "updatedAt", Value: time.Now().UTC()},
	})

	go h.runTranscoder(videoID, video.RawGCSPath)

	jsonOK(w, map[string]any{
		"videoId": videoID, "status": "transcoding",
		"message": "transcoding started — hlsUrl will be available in 1-3 minutes",
	})
}

// ── runTranscoder ─────────────────────────────────────────────────────────────

func (h *handler) runTranscoder(videoID, inputGCSPath string) {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	location := util.Getenv("TRANSCODER_LOCATION", "europe-west1")

	inputURI := fmt.Sprintf("gs://%s/%s", h.bucket, inputGCSPath)
	outputURI := fmt.Sprintf("gs://%s/hls/%s/", h.bucket, videoID)
	hlsURL := fmt.Sprintf("%s/hls/%s/index.m3u8", h.cdnBase, videoID)
	submitURL := fmt.Sprintf(
		"https://transcoder.googleapis.com/v1/projects/%s/locations/%s/jobs",
		projectID, location)

	// FIX: Check file exists in GCS using authenticated HTTP request
	// (not the GCS Go client which may have credential issues)
	authToken, err := getAuthToken(ctx)
	if err != nil {
		log.Printf("video: auth token failed videoId=%s: %v", videoID, err)
		h.markFailed(videoID)
		return
	}

	// Verify file exists via GCS JSON API
	checkURL := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s",
		h.bucket, strings.ReplaceAll(inputGCSPath, "/", "%2F"))
	checkReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	checkReq.Header.Set("Authorization", "Bearer "+authToken)
	checkResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(checkReq)
	if err != nil || checkResp.StatusCode == 404 {
		log.Printf("video: input file NOT found in GCS videoId=%s path=%s status=%v",
			videoID, inputGCSPath, checkResp)
		h.markFailed(videoID)
		return
	}
	checkResp.Body.Close()
	log.Printf("video: ✅ input file verified in GCS videoId=%s", videoID)

	// Submit transcoder job
	jobPayload := map[string]any{
		"inputUri": inputURI, "outputUri": outputURI,
		"elementaryStreams": []map[string]any{
			{"key": "video-720p", "videoStream": map[string]any{"h264": map[string]any{
				"heightPixels": 720, "widthPixels": 1280, "bitrateBps": 1500000, "frameRate": 30}}},
			{"key": "video-480p", "videoStream": map[string]any{"h264": map[string]any{
				"heightPixels": 480, "widthPixels": 854, "bitrateBps": 800000, "frameRate": 30}}},
			{"key": "video-360p", "videoStream": map[string]any{"h264": map[string]any{
				"heightPixels": 360, "widthPixels": 640, "bitrateBps": 400000, "frameRate": 30}}},
			{"key": "audio", "audioStream": map[string]any{"codec": "aac", "bitrateBps": 128000}},
		},
		"muxStreams": []map[string]any{
			{"key": "hls-720p", "container": "ts",
				"elementaryStreams": []string{"video-720p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "6s"}},
			{"key": "hls-480p", "container": "ts",
				"elementaryStreams": []string{"video-480p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "6s"}},
			{"key": "hls-360p", "container": "ts",
				"elementaryStreams": []string{"video-360p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "6s"}},
		},
		"manifests": []map[string]any{
			{"fileName": "index.m3u8", "type": "HLS",
				"muxStreams": []string{"hls-720p", "hls-480p", "hls-360p"}},
		},
	}

	bodyBytes, _ := json.Marshal(map[string]any{"job": jobPayload})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL,
		strings.NewReader(string(bodyBytes)))
	if err != nil {
		log.Printf("video: request build failed videoId=%s: %v", videoID, err)
		h.markFailed(videoID)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("video: API call failed videoId=%s: %v", videoID, err)
		h.markFailed(videoID)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		log.Printf("video: transcode API error %d videoId=%s: %s",
			resp.StatusCode, videoID, string(errBody))
		h.markFailed(videoID)
		return
	}

	var jobResp map[string]any
	json.NewDecoder(resp.Body).Decode(&jobResp)
	jobName, _ := jobResp["name"].(string)
	if jobName == "" {
		log.Printf("video: no job name videoId=%s resp=%v", videoID, jobResp)
		h.markFailed(videoID)
		return
	}

	jobURL := fmt.Sprintf("https://transcoder.googleapis.com/v1/%s", jobName)
	log.Printf("video: transcoding job started videoId=%s job=%s", videoID, jobName)

	// Poll job status
	for i := 0; i < 90; i++ {
		time.Sleep(10 * time.Second)

		pollToken, err := getAuthToken(ctx)
		if err != nil {
			log.Printf("video: poll token failed i=%d: %v", i, err)
			continue
		}
		pollReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, jobURL, nil)
		pollReq.Header.Set("Authorization", "Bearer "+pollToken)
		pollResp, err := client.Do(pollReq)
		if err != nil {
			log.Printf("video: poll request failed i=%d: %v", i, err)
			continue
		}
		var result map[string]any
		json.NewDecoder(pollResp.Body).Decode(&result)
		pollResp.Body.Close()

		state, _ := result["state"].(string)
		log.Printf("video: state=%s videoId=%s poll=%d", state, videoID, i+1)

		if state == "SUCCEEDED" {
			h.fs.Collection("videos").Doc(videoID).Update(ctx, []firestore.Update{
				{Path: "status", Value: "ready"},
				{Path: "hlsUrl", Value: hlsURL},
				{Path: "updatedAt", Value: time.Now().UTC()},
			})
			log.Printf("video: ✅ transcoding complete videoId=%s", videoID)

			// Update linked stream if any
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
			psclient.PublishEvent(ctx, "stream_events", map[string]any{
				"eventType": "video_ready", "videoId": videoID,
				"hlsUrl": hlsURL, "timestamp": time.Now().UTC().Format(time.RFC3339),
			})
			return

		} else if state == "FAILED" {
			// Log full error details
			errDetail, _ := json.Marshal(result["error"])
			log.Printf("video: ❌ transcoding FAILED videoId=%s error=%s", videoID, string(errDetail))
			h.markFailed(videoID)
			return
		}
	}

	log.Printf("video: transcoding timed out videoId=%s", videoID)
	h.markFailed(videoID)
}

func (h *handler) markFailed(videoID string) {
	h.fs.Collection("videos").Doc(videoID).Update(context.Background(), []firestore.Update{
		{Path: "status", Value: "failed"}, {Path: "updatedAt", Value: time.Now().UTC()},
	})
}

// ── GET /api/v1/videos ────────────────────────────────────────────────────────

func (h *handler) listVideos(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("videos").
		Where("status", "==", "ready").
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
		jsonError(w, fmt.Sprintf("video not ready — status: %s", v.Status), http.StatusAccepted)
		return
	}
	jsonOK(w, map[string]string{
		"videoId": videoID, "manifestUrl": v.HLSUrl, "status": v.Status,
	})
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
	role := h.getUserRole(r.Context(), uid)
	if v.UploaderUID != uid && role != "admin" {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	h.fs.Collection("videos").Doc(videoID).Delete(r.Context())
	go func() {
		ctx := context.Background()
		h.gcs.Bucket(h.bucket).Object(v.RawGCSPath).Delete(ctx)
		it := h.gcs.Bucket(h.bucket).Objects(ctx,
			&storage.Query{Prefix: fmt.Sprintf("hls/%s/", videoID)})
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
	v1.HandleFunc("/videos/{id}/manifest", h.getManifest).Methods(http.MethodGet)

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
