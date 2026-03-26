package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub"
	"github.com/gorilla/mux"
	"google.golang.org/api/option"

	fbclient "sports-stream-backend/pkg/firebase"
	"sports-stream-backend/pkg/middleware"
	"sports-stream-backend/pkg/util"
)

// ── Models ────────────────────────────────────────────────────────────────────

type ViewerEvent struct {
	EventType string `json:"eventType"`
	StreamID  string `json:"streamId"`
	UID       string `json:"uid"`
	Timestamp string `json:"timestamp"`
}

type StreamEvent struct {
	EventType string `json:"eventType"`
	StreamID  string `json:"streamId"`
	Title     string `json:"title"`
	Timestamp string `json:"timestamp"`
}

type StreamStats struct {
	StreamID       string    `firestore:"streamId"       json:"streamId"`
	CurrentViewers int       `firestore:"currentViewers" json:"currentViewers"`
	PeakViewers    int       `firestore:"peakViewers"    json:"peakViewers"`
	TotalJoins     int       `firestore:"totalJoins"     json:"totalJoins"`
	UpdatedAt      time.Time `firestore:"updatedAt"      json:"updatedAt"`
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

type handler struct {
	fs *firestore.Client
}

func (h *handler) getStreamStats(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("analytics").Doc(streamID).Get(r.Context())
	if err != nil {
		jsonOK(w, StreamStats{
			StreamID:  streamID,
			UpdatedAt: time.Now().UTC(),
		})
		return
	}
	var stats StreamStats
	snap.DataTo(&stats)
	jsonOK(w, stats)
}

// ── FIX: processViewerEvent ───────────────────────────────────────────────────
// Bug was: analytics doc was never updated because transaction was never called.
// Now: creates doc on first join, increments/decrements correctly, tracks peak.
func processViewerEvent(ctx context.Context, fs *firestore.Client, data []byte) {
	var event ViewerEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("analytics: parse viewer event error: %v", err)
		return
	}
	if event.StreamID == "" {
		return
	}

	ref := fs.Collection("analytics").Doc(event.StreamID)
	err := fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		var stats StreamStats
		if err != nil {
			// First event for this stream — create document
			stats = StreamStats{
				StreamID:  event.StreamID,
				UpdatedAt: time.Now().UTC(),
			}
		} else {
			snap.DataTo(&stats)
		}

		switch event.EventType {
		case "viewer_join":
			stats.CurrentViewers++
			stats.TotalJoins++
			if stats.CurrentViewers > stats.PeakViewers {
				stats.PeakViewers = stats.CurrentViewers
			}
		case "viewer_leave":
			stats.CurrentViewers--
			if stats.CurrentViewers < 0 {
				stats.CurrentViewers = 0
			}
		}
		stats.UpdatedAt = time.Now().UTC()
		return tx.Set(ref, stats)
	})

	if err != nil {
		log.Printf("analytics: transaction error streamId=%s: %v", event.StreamID, err)
	} else {
		log.Printf("analytics: %s → streamId=%s", event.EventType, event.StreamID)
	}
}

// FIX: processStreamEvent — init analytics doc when stream starts
func processStreamEvent(ctx context.Context, fs *firestore.Client, data []byte) {
	var event StreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("analytics: parse stream event error: %v", err)
		return
	}
	if event.StreamID == "" {
		return
	}

	ref := fs.Collection("analytics").Doc(event.StreamID)
	switch event.EventType {
	case "stream_started":
		// Create analytics doc when stream starts
		stats := StreamStats{
			StreamID:  event.StreamID,
			UpdatedAt: time.Now().UTC(),
		}
		if _, err := ref.Set(ctx, stats); err != nil {
			log.Printf("analytics: init failed streamId=%s: %v", event.StreamID, err)
		} else {
			log.Printf("analytics: initialized streamId=%s", event.StreamID)
		}

	case "stream_ended":
		// Reset currentViewers to 0 when stream ends
		ref.Update(ctx, []firestore.Update{
			{Path: "currentViewers", Value: 0},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
		log.Printf("analytics: stream ended streamId=%s", event.StreamID)
	}
}

// ── Pub/Sub subscriptions ─────────────────────────────────────────────────────

func startViewerSub(ctx context.Context, client *pubsub.Client, fs *firestore.Client, subName string) {
	sub := client.Subscription(subName)
	log.Printf("analytics: viewer sub listening: %s", subName)
	sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		processViewerEvent(ctx, fs, msg.Data)
		msg.Ack()
	})
}

func startStreamSub(ctx context.Context, client *pubsub.Client, fs *firestore.Client, subName string) {
	sub := client.Subscription(subName)
	log.Printf("analytics: stream sub listening: %s", subName)
	sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		processStreamEvent(ctx, fs, msg.Data)
		msg.Ack()
	})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8085")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")
	viewerSub := util.Getenv("VIEWER_SUB", "viewer-events-analytics-sub")
	streamSub := util.Getenv("STREAM_SUB", "stream-events-analytics-sub")

	if _, err := fbclient.InitClient(ctx, credsFile); err != nil {
		log.Fatalf("analytics: firebase init: %v", err)
	}

	var fsOpts []option.ClientOption
	if credsFile != "" {
		fsOpts = append(fsOpts, option.WithCredentialsFile(credsFile))
	}
	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("analytics: firestore init: %v", err)
	}
	defer fs.Close()

	var psOpts []option.ClientOption
	if credsFile != "" {
		psOpts = append(psOpts, option.WithCredentialsFile(credsFile))
	}
	psClient, err := pubsub.NewClient(ctx, projectID, psOpts...)
	if err != nil {
		log.Fatalf("analytics: pubsub init: %v", err)
	}
	defer psClient.Close()

	// Start subscribers in background
	go startViewerSub(ctx, psClient, fs, viewerSub)
	go startStreamSub(ctx, psClient, fs, streamSub)

	// HTTP server
	h := &handler{fs: fs}
	r := mux.NewRouter()
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, map[string]string{"service": "analytics-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()
	v1.Use(middleware.AuthRequired)
	v1.HandleFunc("/analytics/stream/{id}", h.getStreamStats).Methods(http.MethodGet)

	log.Printf("analytics-service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("analytics: ListenAndServe: %v", err)
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
