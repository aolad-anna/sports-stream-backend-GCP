package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"firebase.google.com/go/v4/messaging"
	"github.com/gorilla/mux"

	fbclient "sports-stream-backend/pkg/firebase"
	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

// ────────────────────────────────────────────────────────────────────────────
// Models
// ────────────────────────────────────────────────────────────────────────────

type NotificationEvent struct {
	EventType string `json:"eventType"` // stream_started | stream_ended | match_reminder
	StreamID  string `json:"streamId"`
	Title     string `json:"title"`
	Timestamp string `json:"timestamp"`
}

// ────────────────────────────────────────────────────────────────────────────
// FCM sender
// ────────────────────────────────────────────────────────────────────────────

func sendToTopic(ctx context.Context, topic, title, body, streamID string) error {
	msgClient, err := fbclient.GetApp().Messaging(ctx)
	if err != nil {
		return err
	}

	msgID, err := msgClient.Send(ctx, &messaging.Message{
		Topic: topic,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Data: map[string]string{
			"streamId":     streamID,
			"click_action": "OPEN_PLAYER",
		},
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				ChannelID: "live_sports",
			},
		},
	})
	if err != nil {
		return err
	}

	log.Printf(`{"service":"notification-service","level":"info","msg":"fcm accepted","messageId":%q,"topic":%q,"streamId":%q}`,
		msgID, topic, streamID)
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// HTTP handlers
// ────────────────────────────────────────────────────────────────────────────

// POST /api/v1/notifications/test
// Trigger any notification directly from Postman — no Pub/Sub needed
// Body: {"eventType":"stream_started","streamId":"stream_123","title":"Test Match"}
func handleTestNotification(w http.ResponseWriter, r *http.Request) {
	var ev NotificationEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if ev.EventType == "" {
		jsonError(w, "eventType is required", http.StatusBadRequest)
		return
	}

	log.Printf(`{"service":"notification-service","level":"info","msg":"test notification triggered","eventType":%q,"streamId":%q}`,
		ev.EventType, ev.StreamID)

	ok := handleNotificationEvent(mustMarshal(ev))
	if !ok {
		jsonError(w, "failed to send notification — check logs", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{
		"sent":      true,
		"eventType": ev.EventType,
		"streamId":  ev.StreamID,
		"title":     ev.Title,
	})
}

// GET /health
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, map[string]string{"service": "notification-service", "status": "ok"})
}

// ────────────────────────────────────────────────────────────────────────────
// Pub/Sub event handler
// ────────────────────────────────────────────────────────────────────────────

func handleNotificationEvent(data []byte) bool {
	var ev NotificationEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		log.Printf("notification: bad payload (acking to discard): %v", err)
		return true
	}

	log.Printf(`{"service":"notification-service","level":"info","msg":"event received","eventType":%q,"streamId":%q,"title":%q}`,
		ev.EventType, ev.StreamID, ev.Title)

	ctx := context.Background()
	var err error

	switch ev.EventType {
	case "stream_started":
		err = sendToTopic(ctx, "sports_live", "🔴 Match is LIVE!", ev.Title+" has started", ev.StreamID)

	case "stream_ended":
		err = sendToTopic(ctx, "sports_live", "Match Ended", ev.Title+" has ended", ev.StreamID)

	case "match_reminder":
		err = sendToTopic(ctx, "match_reminders", "⚽ Match starting soon!", ev.Title+" starts in 15 minutes", ev.StreamID)

	default:
		log.Printf(`{"service":"notification-service","level":"info","msg":"unknown event type","eventType":%q}`, ev.EventType)
		return true
	}

	if err != nil {
		log.Printf(`{"service":"notification-service","level":"error","msg":"fcm send failed","eventType":%q,"error":%q}`,
			ev.EventType, err.Error())
		return false
	}

	log.Printf(`{"service":"notification-service","level":"info","msg":"notification sent","eventType":%q,"streamId":%q}`,
		ev.EventType, ev.StreamID)
	return true
}

// ────────────────────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")
	notifSub := util.Getenv("NOTIFICATION_SUB", "notification-events-sub")
	port := util.Getenv("PORT", "8083")

	if _, err := fbclient.InitClient(ctx, credsFile); err != nil {
		log.Fatalf("firebase init: %v", err)
	}

	if _, err := psclient.InitClient(ctx, projectID, credsFile); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}

	// Start Pub/Sub subscriber in background
	go func() {
		log.Printf(`{"service":"notification-service","level":"info","msg":"listening","subscription":%q}`, notifSub)
		if err := psclient.Subscribe(ctx, notifSub, handleNotificationEvent); err != nil {
			log.Fatalf("subscription error: %v", err)
		}
	}()

	// Start HTTP server for health check + test endpoint
	r := mux.NewRouter()
	r.HandleFunc("/health", handleHealth).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/notifications/test", handleTestNotification).Methods(http.MethodPost)

	log.Printf(`{"service":"notification-service","level":"info","msg":"http server started","port":%q}`, port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("http server error: %v", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
