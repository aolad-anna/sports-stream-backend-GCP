package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gorilla/mux"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fbclient "sports-stream-backend/pkg/firebase"
	psclient "sports-stream-backend/pkg/pubsub"
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

type AnalyticsDoc struct {
	StreamID       string    `firestore:"streamId"       json:"streamId"`
	CurrentViewers int64     `firestore:"currentViewers" json:"currentViewers"`
	PeakViewers    int64     `firestore:"peakViewers"    json:"peakViewers"`
	TotalJoins     int64     `firestore:"totalJoins"     json:"totalJoins"`
	UpdatedAt      time.Time `firestore:"updatedAt"      json:"updatedAt"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

type handler struct {
	fs *firestore.Client
}

// processViewerEvent — updates currentViewers/peakViewers/totalJoins
// AND writes a join/leave record to analytics/{streamId}/viewerHistory/{uid}

func (h *handler) processViewerEvent(ctx context.Context, data []byte) bool {
	var event ViewerEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("analytics: bad viewer event JSON: %v", err)
		return true
	}
	if event.StreamID == "" || event.UID == "" {
		return true
	}

	ref := h.fs.Collection("analytics").Doc(event.StreamID)

	// ── Update main analytics counters ────────────────────────────────────────
	err := h.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)

		var doc AnalyticsDoc
		if status.Code(err) == codes.NotFound {
			doc = AnalyticsDoc{
				StreamID:       event.StreamID,
				CurrentViewers: 0,
				PeakViewers:    0,
				TotalJoins:     0,
			}
		} else if err != nil {
			return err
		} else {
			if e := snap.DataTo(&doc); e != nil {
				return e
			}
		}

		switch event.EventType {
		case "viewer_join":
			doc.CurrentViewers++
			doc.TotalJoins++
			if doc.CurrentViewers > doc.PeakViewers {
				doc.PeakViewers = doc.CurrentViewers
			}
		case "viewer_leave":
			doc.CurrentViewers--
			if doc.CurrentViewers < 0 {
				doc.CurrentViewers = 0
			}
		default:
			return nil
		}

		doc.UpdatedAt = time.Now().UTC()
		return tx.Set(ref, doc)
	})

	if err != nil {
		log.Printf("analytics: transaction failed streamId=%s event=%s: %v",
			event.StreamID, event.EventType, err)
		return false
	}

	// ── Write viewer history record ───────────────────────────────────────────
	// analytics/{streamId}/viewerHistory/{uid} — one doc per viewer
	// Shows: uid, joinedAt, leftAt (updated on leave), eventCount
	historyRef := ref.Collection("viewerHistory").Doc(event.UID)

	switch event.EventType {
	case "viewer_join":
		joinTime := time.Now().UTC()
		if event.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
				joinTime = t
			}
		}
		// Set/merge — creates on first join, updates eventCount on re-join
		h.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
			snap, err := tx.Get(historyRef)
			if status.Code(err) == codes.NotFound {
				// First time this user joined
				return tx.Set(historyRef, map[string]any{
					"uid":        event.UID,
					"streamId":   event.StreamID,
					"joinedAt":   joinTime,
					"leftAt":     nil,
					"joinCount":  int64(1),
					"isWatching": true,
					"updatedAt":  time.Now().UTC(),
				})
			} else if err != nil {
				return err
			}
			// Re-joined — increment count
			var existing map[string]any
			snap.DataTo(&existing)
			joinCount := int64(1)
			if v, ok := existing["joinCount"]; ok {
				if n, ok := v.(int64); ok {
					joinCount = n + 1
				}
			}
			return tx.Set(historyRef, map[string]any{
				"uid":        event.UID,
				"streamId":   event.StreamID,
				"joinedAt":   joinTime,
				"leftAt":     nil,
				"joinCount":  joinCount,
				"isWatching": true,
				"updatedAt":  time.Now().UTC(),
			})
		})

	case "viewer_leave":
		leaveTime := time.Now().UTC()
		if event.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
				leaveTime = t
			}
		}
		// Update leftAt and isWatching=false
		historyRef.Update(ctx, []firestore.Update{
			{Path: "leftAt", Value: leaveTime},
			{Path: "isWatching", Value: false},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
	}

	log.Printf("analytics: %s → stream=%s uid=%s", event.EventType, event.StreamID, event.UID)
	return true
}

// processStreamEvent handles stream_started and stream_ended.

func (h *handler) processStreamEvent(ctx context.Context, data []byte) bool {
	var event StreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("analytics: bad stream event JSON: %v", err)
		return true
	}
	if event.StreamID == "" {
		return true
	}

	ref := h.fs.Collection("analytics").Doc(event.StreamID)

	switch event.EventType {
	case "stream_started":
		doc := map[string]any{
			"streamId":       event.StreamID,
			"currentViewers": int64(0),
			"peakViewers":    int64(0),
			"totalJoins":     int64(0),
			"updatedAt":      time.Now().UTC(),
		}
		if _, err := ref.Set(ctx, doc, firestore.MergeAll); err != nil {
			log.Printf("analytics: failed to init doc streamId=%s: %v", event.StreamID, err)
			return false
		}
		log.Printf("analytics: initialized doc for stream %s", event.StreamID)

	case "stream_ended":
		doc := map[string]any{
			"currentViewers": int64(0),
			"updatedAt":      time.Now().UTC(),
		}
		if _, err := ref.Set(ctx, doc, firestore.MergeAll); err != nil {
			log.Printf("analytics: failed to reset currentViewers streamId=%s: %v", event.StreamID, err)
			return false
		}
		log.Printf("analytics: stream ended → currentViewers reset for %s", event.StreamID)
	}

	return true
}

// GET /api/v1/analytics/stream/:id — returns main analytics doc

func (h *handler) getStreamStats(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("analytics").Doc(streamID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		jsonOK(w, AnalyticsDoc{
			StreamID:       streamID,
			CurrentViewers: 0,
			PeakViewers:    0,
			TotalJoins:     0,
		})
		return
	}
	if err != nil {
		jsonError(w, "firestore error", http.StatusInternalServerError)
		return
	}
	var doc AnalyticsDoc
	snap.DataTo(&doc)
	jsonOK(w, doc)
}

// GET /api/v1/analytics/stream/:id/viewers — returns full viewer history list

func (h *handler) getViewerHistory(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]

	docs, err := h.fs.Collection("analytics").Doc(streamID).
		Collection("viewerHistory").
		OrderBy("joinedAt", firestore.Asc).
		Documents(r.Context()).GetAll()

	if err != nil {
		jsonError(w, "firestore error", http.StatusInternalServerError)
		return
	}

	viewers := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		var v map[string]any
		if err := d.DataTo(&v); err == nil {
			viewers = append(viewers, v)
		}
	}

	jsonOK(w, map[string]any{
		"streamId":    streamID,
		"totalJoined": len(viewers),
		"viewers":     viewers,
	})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8085")
	credsValue := util.Getenv("FIREBASE_CREDENTIALS", "")

	var credOpt option.ClientOption
	if strings.HasPrefix(strings.TrimSpace(credsValue), "{") {
		credOpt = option.WithCredentialsJSON([]byte(credsValue))
	} else if credsValue != "" {
		credOpt = option.WithCredentialsFile(credsValue)
	}

	if _, err := fbclient.InitClient(ctx, credsValue); err != nil {
		log.Fatalf("analytics: firebase init: %v", err)
	}
	if _, err := psclient.InitClient(ctx, projectID, credsValue); err != nil {
		log.Fatalf("analytics: pubsub init: %v", err)
	}

	var fsOpts []option.ClientOption
	if credOpt != nil {
		fsOpts = append(fsOpts, credOpt)
	}
	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("analytics: firestore init: %v", err)
	}
	defer fs.Close()

	h := &handler{fs: fs}

	// ── Pub/Sub subscriptions ──────────────────────────────────────────────
	viewerSub := util.Getenv("VIEWER_SUB", "viewer-events-analytics-sub")
	go func() {
		log.Printf("analytics: listening on viewer sub: %s", viewerSub)
		for {
			if err := psclient.Subscribe(ctx, viewerSub, func(data []byte) bool {
				return h.processViewerEvent(ctx, data)
			}); err != nil {
				log.Printf("analytics: viewer sub error: %v — retrying in 5s", err)
				time.Sleep(5 * time.Second)
			}
		}
	}()

	streamSub := util.Getenv("STREAM_SUB", "stream-events-analytics-sub")
	go func() {
		log.Printf("analytics: listening on stream sub: %s", streamSub)
		for {
			if err := psclient.Subscribe(ctx, streamSub, func(data []byte) bool {
				return h.processStreamEvent(ctx, data)
			}); err != nil {
				log.Printf("analytics: stream sub error: %v — retrying in 5s", err)
				time.Sleep(5 * time.Second)
			}
		}
	}()

	// ── HTTP ───────────────────────────────────────────────────────────────
	r := mux.NewRouter()
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, map[string]string{"service": "analytics-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()
	v1.HandleFunc("/analytics/stream/{id}", h.getStreamStats).Methods(http.MethodGet)
	v1.HandleFunc("/analytics/stream/{id}/viewers", h.getViewerHistory).Methods(http.MethodGet)

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
