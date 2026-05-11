package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gorilla/mux"
	"golang.org/x/oauth2/google"
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
	ID             string    `firestore:"id"            json:"id"`
	Title          string    `firestore:"title"         json:"title"`
	Status         string    `firestore:"status"        json:"status"`
	HLSUrl         string    `firestore:"hlsUrl"        json:"hlsUrl"`
	RTMPIngestUrl  string    `firestore:"rtmpIngestUrl" json:"rtmpIngestUrl,omitempty"`
	ChannelName    string    `firestore:"channelName"   json:"channelName,omitempty"`
	InputName      string    `firestore:"inputName"     json:"inputName,omitempty"`
	ViewerCount    int       `firestore:"viewerCount"   json:"viewerCount"`
	BroadcasterUID string    `firestore:"broadcasterUid" json:"broadcasterUid"`
	CreatedAt      time.Time `firestore:"createdAt"     json:"createdAt"`
	UpdatedAt      time.Time `firestore:"updatedAt"     json:"updatedAt"`
}

type CreateStreamRequest struct {
	Title string `json:"title"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

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

// ── getAuthToken ──────────────────────────────────────────────────────────────

func getAuthToken(ctx context.Context) (string, error) {
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", fmt.Errorf("token source: %w", err)
	}
	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("get token: %w", err)
	}
	return tok.AccessToken, nil
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
		ID: streamID, Title: req.Title, Status: "live",
		ViewerCount: 0, BroadcasterUID: uid, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.fs.Collection("streams").Doc(streamID).Set(r.Context(), stream); err != nil {
		jsonError(w, "failed to create stream", http.StatusInternalServerError)
		return
	}

	// Start GCP Live Stream channel — gets RTMP ingest URL automatically
	go startLiveChannel(streamID, req.Title, h.fs)

	go psclient.PublishEvent(context.Background(), "stream_events", map[string]any{
		"eventType": "stream_started", "streamId": streamID,
		"title": req.Title, "uid": uid, "timestamp": now.Format(time.RFC3339),
	})
	jsonOK(w, stream)
}

// ── stopStream ────────────────────────────────────────────────────────────────

func (h *handler) stopStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	snap, err := h.fs.Collection("streams").Doc(streamID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonError(w, "stream not found", http.StatusNotFound)
		return
	}
	var s Stream
	snap.DataTo(&s)

	if s.BroadcasterUID != uid && h.getUserRole(r.Context(), uid) != "admin" {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}

	go stopLiveChannel(streamID, h.fs)
	jsonOK(w, map[string]any{"stopping": true, "streamId": streamID})
}

// ── listStreams ───────────────────────────────────────────────────────────────

func (h *handler) listStreams(w http.ResponseWriter, r *http.Request) {
	docs, err := h.fs.Collection("streams").
		Where("status", "==", "live").Limit(50).
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
	snap.DataTo(&s)
	jsonOK(w, s)
}

// ── joinStream ────────────────────────────────────────────────────────────────

func (h *handler) joinStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())
	streamRef := h.fs.Collection("streams").Doc(streamID)
	viewerRef := streamRef.Collection("viewers").Doc(uid)
	var newCount int
	err := h.fs.RunTransaction(r.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
		streamSnap, err := tx.Get(streamRef)
		if err != nil {
			return err
		}
		var s Stream
		streamSnap.DataTo(&s)
		if s.Status == "ended" {
			return fmt.Errorf("stream has ended")
		}
		viewerSnap, _ := tx.Get(viewerRef)
		if viewerSnap.Exists() {
			newCount = s.ViewerCount
			return nil
		}
		newCount = s.ViewerCount + 1
		tx.Set(viewerRef, map[string]any{"uid": uid, "joinedAt": time.Now().UTC()})
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
	go psclient.PublishEvent(context.Background(), "viewer_events", map[string]any{
		"eventType": "viewer_join", "streamId": streamID,
		"uid": uid, "timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	jsonOK(w, map[string]any{"joined": true, "viewerCount": newCount})
}

// ── leaveStream ───────────────────────────────────────────────────────────────

func (h *handler) leaveStream(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())
	streamRef := h.fs.Collection("streams").Doc(streamID)
	viewerRef := streamRef.Collection("viewers").Doc(uid)
	var newCount int
	err := h.fs.RunTransaction(r.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
		viewerSnap, _ := tx.Get(viewerRef)
		if !viewerSnap.Exists() {
			return nil
		}
		streamSnap, err := tx.Get(streamRef)
		if err != nil {
			return err
		}
		var s Stream
		streamSnap.DataTo(&s)
		newCount = s.ViewerCount - 1
		if newCount < 0 {
			newCount = 0
		}
		tx.Delete(viewerRef)
		return tx.Update(streamRef, []firestore.Update{
			{Path: "viewerCount", Value: newCount},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
	})
	if err != nil {
		log.Printf("leaveStream error streamId=%s: %v", streamID, err)
		jsonError(w, "failed to leave stream", http.StatusInternalServerError)
		return
	}
	go psclient.PublishEvent(context.Background(), "viewer_events", map[string]any{
		"eventType": "viewer_leave", "streamId": streamID,
		"uid": uid, "timestamp": time.Now().UTC().Format(time.RFC3339),
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
		jsonError(w, "stream not ready yet — RTMP channel starting", http.StatusAccepted)
		return
	}
	jsonOK(w, map[string]string{"streamId": streamID, "manifestUrl": s.HLSUrl})
}

// ── resetViewerCount ──────────────────────────────────────────────────────────

func (h *handler) resetViewerCount(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())
	if h.getUserRole(r.Context(), uid) != "admin" {
		jsonError(w, "admin only", http.StatusForbidden)
		return
	}
	_, err := h.fs.Collection("streams").Doc(streamID).Update(r.Context(), []firestore.Update{
		{Path: "viewerCount", Value: 0}, {Path: "updatedAt", Value: time.Now().UTC()},
	})
	if err != nil {
		jsonError(w, "failed to reset", http.StatusInternalServerError)
		return
	}
	go func() {
		ctx := context.Background()
		docs, err := h.fs.Collection("streams").Doc(streamID).
			Collection("viewers").Documents(ctx).GetAll()
		if err != nil || len(docs) == 0 {
			return
		}
		batch := h.fs.Batch()
		for i, d := range docs {
			batch.Delete(d.Ref)
			if (i+1)%500 == 0 {
				batch.Commit(ctx)
				batch = h.fs.Batch()
			}
		}
		if len(docs)%500 != 0 {
			batch.Commit(ctx)
		}
	}()
	jsonOK(w, map[string]any{"reset": true, "viewerCount": 0})
}

// ── GCP Live Stream API ───────────────────────────────────────────────────────

const liveStreamAPIBase = "https://livestream.googleapis.com/v1"

// startLiveChannel creates a GCP Live Stream channel.
// GCP provides the RTMP ingest URL automatically — no server needed.
// Broadcaster uses this URL in OBS or Larix to push their stream.

func startLiveChannel(streamID, title string, fs *firestore.Client) {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	gcsBucket := util.Getenv("GCS_BUCKET", "sports-stream-66553.appspot.com")
	location := util.Getenv("LIVESTREAM_LOCATION", "us-central1")

	authToken, err := getAuthToken(ctx)
	if err != nil {
		log.Printf("livestream: auth token failed streamId=%s: %v", streamID, err)
		return
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// ── Step 1: Create Input (RTMP endpoint) ──────────────────────────────────
	inputID := fmt.Sprintf("input-%s", streamID)
	inputURL := fmt.Sprintf("%s/projects/%s/locations/%s/inputs?inputId=%s",
		liveStreamAPIBase, projectID, location, inputID)

	inputBody, _ := json.Marshal(map[string]any{"type": "RTMP_PUSH"})
	inputReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, inputURL,
		bytes.NewReader(inputBody))
	inputReq.Header.Set("Authorization", "Bearer "+authToken)
	inputReq.Header.Set("Content-Type", "application/json")

	inputResp, err := client.Do(inputReq)
	if err != nil {
		log.Printf("livestream: create input failed streamId=%s: %v", streamID, err)
		return
	}
	inputRespBody, _ := io.ReadAll(inputResp.Body)
	inputResp.Body.Close()

	if inputResp.StatusCode >= 400 {
		log.Printf("livestream: create input error %d streamId=%s: %s",
			inputResp.StatusCode, streamID, string(inputRespBody))
		return
	}

	var inputResult map[string]any
	json.Unmarshal(inputRespBody, &inputResult)
	inputName, _ := inputResult["name"].(string)
	log.Printf("livestream: input created=%s streamId=%s", inputName, streamID)

	// ── Step 2: Wait for input to be ready and get RTMP URI ───────────────────
	rtmpIngestURL := waitForInputReady(ctx, client, inputName)
	if rtmpIngestURL == "" {
		log.Printf("livestream: input never became ready streamId=%s", streamID)
		return
	}
	log.Printf("livestream: RTMP URL=%s streamId=%s", rtmpIngestURL, streamID)

	// ── Step 3: Create Channel ────────────────────────────────────────────────
	channelID := fmt.Sprintf("channel-%s", streamID)
	channelURL := fmt.Sprintf("%s/projects/%s/locations/%s/channels?channelId=%s",
		liveStreamAPIBase, projectID, location, channelID)

	hlsOutputURI := fmt.Sprintf("gs://%s/live/%s/", gcsBucket, streamID)
	hlsURL := fmt.Sprintf("https://storage.googleapis.com/%s/live/%s/manifest.m3u8",
		gcsBucket, streamID)

	channelPayload := map[string]any{
		"inputAttachments": []map[string]any{
			{"key": "input-0", "input": inputName},
		},
		"output": map[string]any{"uri": hlsOutputURI},
		"elementaryStreams": []map[string]any{
			{"key": "video-hd", "videoStream": map[string]any{"h264": map[string]any{
				"profile": "high", "bitrateBps": 3000000,
				"frameRate": 30, "heightPixels": 720, "widthPixels": 1280,
			}}},
			{"key": "video-sd", "videoStream": map[string]any{"h264": map[string]any{
				"profile": "main", "bitrateBps": 1000000,
				"frameRate": 30, "heightPixels": 480, "widthPixels": 854,
			}}},
			{"key": "audio-aac", "audioStream": map[string]any{
				"codec": "aac", "bitrateBps": 128000, "channelCount": 2,
			}},
		},
		"muxStreams": []map[string]any{
			{"key": "hls-hd", "container": "ts",
				"elementaryStreams": []string{"video-hd", "audio-aac"},
				"segmentSettings":   map[string]any{"segmentDuration": "2s"}},
			{"key": "hls-sd", "container": "ts",
				"elementaryStreams": []string{"video-sd", "audio-aac"},
				"segmentSettings":   map[string]any{"segmentDuration": "2s"}},
		},
		"manifests": []map[string]any{
			{"fileName": "manifest.m3u8", "type": "HLS",
				"muxStreams":      []string{"hls-hd", "hls-sd"},
				"maxSegmentCount": 10},
		},
	}

	channelBody, _ := json.Marshal(channelPayload)
	channelReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, channelURL,
		bytes.NewReader(channelBody))
	channelReq.Header.Set("Authorization", "Bearer "+authToken)
	channelReq.Header.Set("Content-Type", "application/json")

	channelResp, err := client.Do(channelReq)
	if err != nil {
		log.Printf("livestream: create channel failed streamId=%s: %v", streamID, err)
		return
	}
	channelRespBody, _ := io.ReadAll(channelResp.Body)
	channelResp.Body.Close()

	if channelResp.StatusCode >= 400 {
		log.Printf("livestream: create channel error %d streamId=%s: %s",
			channelResp.StatusCode, streamID, string(channelRespBody))
		return
	}

	var channelResult map[string]any
	json.Unmarshal(channelRespBody, &channelResult)
	channelName, _ := channelResult["name"].(string)
	log.Printf("livestream: channel created=%s streamId=%s", channelName, streamID)

	// ── Step 4: Start Channel ──────────────────────────────────────────────────
	startURL := fmt.Sprintf("%s/%s:start", liveStreamAPIBase, channelName)
	startReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, startURL,
		bytes.NewReader([]byte("{}")))
	startReq.Header.Set("Authorization", "Bearer "+authToken)
	startReq.Header.Set("Content-Type", "application/json")

	startResp, err := client.Do(startReq)
	if err != nil {
		log.Printf("livestream: start channel failed streamId=%s: %v", streamID, err)
		return
	}
	startResp.Body.Close()
	log.Printf("livestream: channel started streamId=%s", streamID)

	// ── Step 5: Save to Firestore ─────────────────────────────────────────────
	fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
		{Path: "hlsUrl", Value: hlsURL},
		{Path: "rtmpIngestUrl", Value: rtmpIngestURL},
		{Path: "channelName", Value: channelName},
		{Path: "inputName", Value: inputName},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	log.Printf("livestream: ✅ ready streamId=%s rtmp=%s hls=%s",
		streamID, rtmpIngestURL, hlsURL)

	// Monitor channel in background
	go monitorChannel(ctx, client, channelName, streamID, fs)
}

// waitForInputReady polls until the input RTMP URI is available.

func waitForInputReady(ctx context.Context, client *http.Client, inputName string) string {
	url := fmt.Sprintf("%s/%s", liveStreamAPIBase, inputName)
	for i := 0; i < 30; i++ {
		time.Sleep(5 * time.Second)
		tok, err := getAuthToken(ctx)
		if err != nil {
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		// URI is at top level
		if uri, ok := result["uri"].(string); ok && uri != "" {
			return uri
		}
		log.Printf("livestream: waiting for RTMP URI poll=%d", i+1)
	}
	return ""
}

// stopLiveChannel stops the GCP Live Stream channel.

func stopLiveChannel(streamID string, fs *firestore.Client) {
	ctx := context.Background()
	snap, err := fs.Collection("streams").Doc(streamID).Get(ctx)
	if err != nil {
		return
	}
	var s Stream
	snap.DataTo(&s)
	if s.ChannelName == "" {
		return
	}

	tok, err := getAuthToken(ctx)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 30 * time.Second}

	// Stop channel
	stopURL := fmt.Sprintf("%s/%s:stop", liveStreamAPIBase, s.ChannelName)
	stopReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, stopURL,
		bytes.NewReader([]byte("{}")))
	stopReq.Header.Set("Authorization", "Bearer "+tok)
	stopReq.Header.Set("Content-Type", "application/json")
	stopResp, _ := client.Do(stopReq)
	if stopResp != nil {
		stopResp.Body.Close()
	}
	log.Printf("livestream: stopped streamId=%s", streamID)

	// Update Firestore
	fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
		{Path: "status", Value: "ended"},
		{Path: "viewerCount", Value: 0},
		{Path: "updatedAt", Value: time.Now().UTC()},
	})

	// Cleanup GCP resources after 60s
	go func() {
		time.Sleep(60 * time.Second)
		cleanTok, _ := getAuthToken(ctx)

		// Delete channel
		delCh, _ := http.NewRequestWithContext(ctx, http.MethodDelete,
			fmt.Sprintf("%s/%s", liveStreamAPIBase, s.ChannelName), nil)
		delCh.Header.Set("Authorization", "Bearer "+cleanTok)
		r1, _ := client.Do(delCh)
		if r1 != nil {
			r1.Body.Close()
		}

		// Delete input
		delIn, _ := http.NewRequestWithContext(ctx, http.MethodDelete,
			fmt.Sprintf("%s/%s", liveStreamAPIBase, s.InputName), nil)
		delIn.Header.Set("Authorization", "Bearer "+cleanTok)
		r2, _ := client.Do(delIn)
		if r2 != nil {
			r2.Body.Close()
		}
		log.Printf("livestream: cleaned up streamId=%s", streamID)
	}()
}

// monitorChannel watches the channel state every 30s.

func monitorChannel(ctx context.Context, client *http.Client,
	channelName, streamID string, fs *firestore.Client) {

	url := fmt.Sprintf("%s/%s", liveStreamAPIBase, channelName)
	for i := 0; i < 960; i++ { // 8 hours max
		time.Sleep(30 * time.Second)
		tok, err := getAuthToken(ctx)
		if err != nil {
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		state, _ := result["streamingState"].(string)
		log.Printf("livestream: state=%s streamId=%s", state, streamID)

		if state == "STOPPED" || state == "STARTING_STOPPED" {
			fs.Collection("streams").Doc(streamID).Update(ctx, []firestore.Update{
				{Path: "status", Value: "ended"},
				{Path: "viewerCount", Value: 0},
				{Path: "updatedAt", Value: time.Now().UTC()},
			})
			psclient.PublishEvent(ctx, "stream_events", map[string]any{
				"eventType": "stream_ended", "streamId": streamID,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			})
			return
		}
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8082")
	creds := util.Getenv("FIREBASE_CREDENTIALS", "")

	if _, err := fbclient.InitClient(ctx, creds); err != nil {
		log.Fatalf("firebase init: %v", err)
	}
	if _, err := psclient.InitClient(ctx, projectID, creds); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}
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

	h := &handler{fs: fs}
	r := mux.NewRouter()

	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := fs.Collection("streams").Limit(1).Documents(healthCtx).Next()
		if err != nil && err != iterator.Done {
			jsonError(w, "firestore unreachable", http.StatusServiceUnavailable)
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
	protected.HandleFunc("/streams/{id}/stop", h.stopStream).Methods(http.MethodPost)
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
