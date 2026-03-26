package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/api/option"

	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

// ────────────────────────────────────────────────────────────────────────────
// Prometheus metrics — 4 Golden Signals
// ────────────────────────────────────────────────────────────────────────────

var (
	// SATURATION — live viewer count per stream_id (feeds HPA Trigger 4)
	liveViewers = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "live_concurrent_viewers",
		Help: "Number of viewers currently watching a stream.",
	}, []string{"stream_id"})

	// TRAFFIC — total events processed by type
	eventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stream_events_total",
		Help: "Total Pub/Sub events processed.",
	}, []string{"event_type"})

	// LATENCY — event processing time
	joinLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "viewer_join_latency_ms",
		Help:    "Milliseconds from viewer_join event receipt to processing.",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000},
	})
)

func init() {
	prometheus.MustRegister(liveViewers, eventsTotal, joinLatency)
}

// ────────────────────────────────────────────────────────────────────────────
// In-memory counters per stream
// ────────────────────────────────────────────────────────────────────────────

var (
	viewerCounters sync.Map // map[streamID]*atomic.Int64
	totalJoins     sync.Map
	peakViewers    sync.Map
)

func getInt64(m *sync.Map, key string) *atomic.Int64 {
	v, _ := m.LoadOrStore(key, &atomic.Int64{})
	return v.(*atomic.Int64)
}

// ────────────────────────────────────────────────────────────────────────────
// Models
// ────────────────────────────────────────────────────────────────────────────

type PubSubEvent struct {
	EventType string `json:"eventType"`
	StreamID  string `json:"streamId"`
	UID       string `json:"uid"`
	Timestamp string `json:"timestamp"`
}

type StreamStats struct {
	StreamID       string    `firestore:"streamId"       json:"streamId"`
	PeakViewers    int64     `firestore:"peakViewers"    json:"peakViewers"`
	TotalJoins     int64     `firestore:"totalJoins"     json:"totalJoins"`
	CurrentViewers int64     `firestore:"currentViewers" json:"currentViewers"`
	UpdatedAt      time.Time `firestore:"updatedAt"      json:"updatedAt"`
}

// ────────────────────────────────────────────────────────────────────────────
// Pub/Sub event handlers
// ────────────────────────────────────────────────────────────────────────────

func handleViewerEvent(data []byte) bool {
	start := time.Now()
	var ev PubSubEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		log.Printf("analytics: bad viewer event: %v", err)
		return false
	}

	sid := ev.StreamID
	switch ev.EventType {
	case "viewer_join":
		count := getInt64(&viewerCounters, sid).Add(1)
		getInt64(&totalJoins, sid).Add(1)
		// Update peak atomically
		peak := getInt64(&peakViewers, sid)
		for {
			old := peak.Load()
			if count <= old {
				break
			}
			if peak.CompareAndSwap(old, count) {
				break
			}
		}
		liveViewers.WithLabelValues(sid).Set(float64(count))
		joinLatency.Observe(float64(time.Since(start).Milliseconds()))

	case "viewer_leave":
		count := getInt64(&viewerCounters, sid).Add(-1)
		if count < 0 {
			getInt64(&viewerCounters, sid).Store(0)
			count = 0
		}
		liveViewers.WithLabelValues(sid).Set(float64(count))
	}

	eventsTotal.WithLabelValues(ev.EventType).Inc()
	return true
}

func handleStreamEvent(data []byte, fs *firestore.Client) bool {
	var ev PubSubEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return false
	}
	eventsTotal.WithLabelValues(ev.EventType).Inc()

	if ev.EventType == "stream_ended" {
		stats := StreamStats{
			StreamID:       ev.StreamID,
			PeakViewers:    getInt64(&peakViewers, ev.StreamID).Load(),
			TotalJoins:     getInt64(&totalJoins, ev.StreamID).Load(),
			CurrentViewers: 0,
			UpdatedAt:      time.Now().UTC(),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := fs.Collection("analytics").Doc(ev.StreamID).Set(ctx, stats); err != nil {
			log.Printf("analytics: firestore write: %v", err)
		}
		viewerCounters.Delete(ev.StreamID)
		totalJoins.Delete(ev.StreamID)
		peakViewers.Delete(ev.StreamID)
		liveViewers.DeleteLabelValues(ev.StreamID)
	}
	return true
}

// ────────────────────────────────────────────────────────────────────────────
// HTTP — GET /api/v1/analytics/stream/:id
// Android admin screen calls this to show peak viewers and total joins.
// ────────────────────────────────────────────────────────────────────────────

func getStreamStats(fs *firestore.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid := mux.Vars(r)["id"]

		snap, err := fs.Collection("analytics").Doc(sid).Get(r.Context())
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			// Still live — return in-memory counters
			stats := StreamStats{
				StreamID:       sid,
				PeakViewers:    getInt64(&peakViewers, sid).Load(),
				TotalJoins:     getInt64(&totalJoins, sid).Load(),
				CurrentViewers: getInt64(&viewerCounters, sid).Load(),
				UpdatedAt:      time.Now().UTC(),
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "data": stats})
			return
		}

		var stats StreamStats
		snap.DataTo(&stats)
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": stats})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8085")
	metricsPort := util.Getenv("METRICS_PORT", "9090")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")

	// Pub/Sub — pass credentials explicitly
	if _, err := psclient.InitClient(ctx, projectID, credsFile); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}

	// Firestore — pass credentials explicitly
	var fsOpts []option.ClientOption
	if credsFile != "" {
		if strings.HasPrefix(strings.TrimSpace(credsFile), "{") {
			fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(credsFile)))
		} else {
			fsOpts = append(fsOpts, option.WithCredentialsFile(credsFile))
		}
	}
	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("firestore init: %v", err)
	}
	defer fs.Close()

	// 4 competing consumer goroutines on viewer events (competing consumers pattern)
	viewerSub := util.Getenv("VIEWER_SUB", "viewer-events-analytics-sub")
	for i := 0; i < 4; i++ {
		go func() {
			if err := psclient.Subscribe(ctx, viewerSub, handleViewerEvent); err != nil {
				log.Printf("viewer sub error: %v", err)
			}
		}()
	}

	// Stream events subscriber
	streamSub := util.Getenv("STREAM_SUB", "stream-events-analytics-sub")
	go func() {
		if err := psclient.Subscribe(ctx, streamSub, func(data []byte) bool {
			return handleStreamEvent(data, fs)
		}); err != nil {
			log.Printf("stream sub error: %v", err)
		}
	}()

	// Prometheus metrics on separate port — scraped by prometheus-adapter for HPA Trigger 4
	go func() {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.Handler())
		log.Printf("analytics-service metrics on :%s/metrics", metricsPort)
		http.ListenAndServe(":"+metricsPort, metricsMux)
	}()

	// REST API
	r := mux.NewRouter()
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"service": "analytics-service", "status": "ok"})
	}).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()
	v1.HandleFunc("/analytics/stream/{id}", getStreamStats(fs)).Methods(http.MethodGet)

	log.Printf("analytics-service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
