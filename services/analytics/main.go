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
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fbclient "sports-stream-backend/pkg/firebase"
	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

// ── Models ────────────────────────────────────────────────────────────────────

type ViewerEvent struct {
	EventType string `json:"eventType"` // viewer_join | viewer_leave
	StreamID  string `json:"streamId"`
	UID       string `json:"uid"`
	Timestamp string `json:"timestamp"`
}

type StreamEvent struct {
	EventType string `json:"eventType"` // stream_started | stream_ended
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

// processViewerEvent updates analytics/streamId on each viewer join or leave.
// BUG FIX 1: Old code used a plain Update which failed silently when doc didn't exist.
// BUG FIX 2: Now uses RunTransaction + Set so it creates doc on first viewer.
// BUG FIX 3: currentViewers has a floor of 0 — can never go negative.
// BUG FIX 4: PeakViewers is correctly tracked as max(current, peak).
func (h *handler) processViewerEvent(ctx context.Context, data []byte) bool {
	var event ViewerEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("analytics: bad viewer event JSON: %v", err)
		return true // ack to discard unparseable message
	}
	if event.StreamID == "" {
		return true
	}

	ref := h.fs.Collection("analytics").Doc(event.StreamID)

	err := h.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)

		var doc AnalyticsDoc
		if status.Code(err) == codes.NotFound {
			// Doc does not exist yet — initialize it
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
		return tx.Set(ref, doc) // creates or overwrites
	})

	if err != nil {
		log.Printf("analytics: transaction failed streamId=%s event=%s: %v",
			event.StreamID, event.EventType, err)
		return false // nack — let Pub/Sub retry
	}

	log.Printf("analytics: %s → stream=%s", event.EventType, event.StreamID)
	return true
}

// processStreamEvent handles stream_started and stream_ended.
// BUG FIX 5: stream_started now creates the analytics doc immediately,
//
//	so it's never missing from the analytics collection.
//
// BUG FIX 6: stream_ended resets currentViewers to 0.
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
		// Create analytics doc as soon as stream starts
		// MergeAll means if doc already exists we don't overwrite existing stats
		doc := AnalyticsDoc{
			StreamID:       event.StreamID,
			CurrentViewers: 0,
			PeakViewers:    0,
			TotalJoins:     0,
			UpdatedAt:      time.Now().UTC(),
		}
		if _, err := ref.Set(ctx, doc, firestore.MergeAll); err != nil {
			log.Printf("analytics: failed to init doc streamId=%s: %v", event.StreamID, err)
			return false
		}
		log.Printf("analytics: initialized doc for stream %s", event.StreamID)

	case "stream_ended":
		// Reset currentViewers — stream is over
		if _, err := ref.Set(ctx, map[string]any{
			"currentViewers": 0,
			"updatedAt":      time.Now().UTC(),
		}, firestore.MergeAll); err != nil {
			log.Printf("analytics: failed to reset currentViewers streamId=%s: %v", event.StreamID, err)
			return false
		}
		log.Printf("analytics: stream ended → currentViewers reset for %s", event.StreamID)
	}

	return true
}

// GET /api/v1/analytics/stream/:id
func (h *handler) getStreamStats(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("analytics").Doc(streamID).Get(r.Context())
	if status.Code(err) == codes.NotFound {
		// Return zero-state doc rather than 404 — valid when stream never had viewers
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

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8085")
	credsValue := util.Getenv("FIREBASE_CREDENTIALS", "")

	// Support both JSON string and file path for credentials
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

	// ── Subscribe to viewer_events Pub/Sub topic ───────────────────────────
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

	// ── Subscribe to stream_events Pub/Sub topic ───────────────────────────
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
		healthCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := fs.Collection("analytics").Limit(1).Documents(healthCtx).Next()
		if err != nil && err != iterator.Done {
			jsonError(w, "health check failed: firestore unreachable", http.StatusServiceUnavailable)
			return
		}

		jsonOK(w, map[string]string{"service": "analytics-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()
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
