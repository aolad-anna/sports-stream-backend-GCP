package main

import (
	"context"
	"encoding/json"
	"log"

	"firebase.google.com/go/v4/messaging"

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

// sendToTopic sends an FCM topic message.
// Android FCMService.onMessageReceived reads notification.title and data["streamId"].
// AndroidConfig.Priority = "high" wakes the device even in Doze mode.
func sendToTopic(ctx context.Context, topic, title, body, streamID string) error {
	msgClient, err := fbclient.GetApp().Messaging(ctx)

	if err != nil {
		return err
	}
	_, err = msgClient.Send(ctx, &messaging.Message{
		Topic: topic,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		// data payload — Android FCMService reads streamId to open PlayerActivity
		Data: map[string]string{
			"streamId":     streamID,
			"click_action": "OPEN_PLAYER",
		},
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				ChannelID: "live_sports", // matches channel ID in Android FCMService.kt
			},
		},
	})
	return err
}

// ────────────────────────────────────────────────────────────────────────────
// Event handler
// ────────────────────────────────────────────────────────────────────────────

func handleNotificationEvent(data []byte) bool {
	var ev NotificationEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		log.Printf("notification: bad payload (acking to discard): %v", err)
		return true // ack bad messages — don't retry garbage data
	}

	ctx := context.Background()
	var err error

	switch ev.EventType {
	case "stream_started":
		// Sent to FCM topic "sports_live" — all Android users who subscribed to this topic
		err = sendToTopic(ctx, "sports_live", "🔴 Match is LIVE!", ev.Title+" has started", ev.StreamID)

	case "stream_ended":
		err = sendToTopic(ctx, "sports_live", "Match Ended", ev.Title+" has ended", ev.StreamID)

	case "match_reminder":
		// Published by Pre-Scaler 15 minutes before kickoff
		err = sendToTopic(ctx, "match_reminders", "⚽ Match starting soon!", ev.Title+" starts in 15 minutes", ev.StreamID)

	default:
		log.Printf(`{"service":"notification-service","level":"info","msg":"unknown event type","eventType":%q}`, ev.EventType)
		return true // ack unknown events — don't clog the queue
	}

	if err != nil {
		log.Printf(`{"service":"notification-service","level":"error","msg":"fcm send failed","eventType":%q,"error":%q}`, ev.EventType, err.Error())
		return false // nack — Pub/Sub retries up to 5 times before routing to DLQ
	}

	log.Printf(`{"service":"notification-service","level":"info","msg":"notification sent","eventType":%q,"streamId":%q}`, ev.EventType, ev.StreamID)
	return true
}

// ────────────────────────────────────────────────────────────────────────────
// Main — no HTTP server, pure Pub/Sub consumer
// ────────────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")
	notifSub := util.Getenv("NOTIFICATION_SUB", "notification-events-sub")

	// Firebase Admin — needed for FCM messaging client
	if _, err := fbclient.InitClient(ctx, credsFile); err != nil {
		log.Fatalf("firebase init: %v", err)
	}

	// Pub/Sub — pass credentials explicitly
	if _, err := psclient.InitClient(ctx, projectID, credsFile); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}

	log.Printf(`{"service":"notification-service","level":"info","msg":"listening","subscription":%q}`, notifSub)

	// Blocking — processes notifications forever. No HTTP server needed.
	if err := psclient.Subscribe(ctx, notifSub, handleNotificationEvent); err != nil {
		log.Fatalf("subscription error: %v", err)
	}
}
