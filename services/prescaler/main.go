package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	psclient "sports-stream-backend/pkg/pubsub"
	"sports-stream-backend/pkg/util"
)

// ────────────────────────────────────────────────────────────────────────────
// Models
// ────────────────────────────────────────────────────────────────────────────

// Match matches the Firestore matches/{id} document.
type Match struct {
	ID          string    `firestore:"id"`
	Title       string    `firestore:"title"`
	ScheduledAt time.Time `firestore:"scheduledAt"`
	Status      string    `firestore:"status"` // scheduled | live | ended
}

// ────────────────────────────────────────────────────────────────────────────
// Firestore — upcoming match query
// ────────────────────────────────────────────────────────────────────────────

// getUpcomingMatches returns matches starting within the next 15 minutes.
func getUpcomingMatches(ctx context.Context, fs *firestore.Client) ([]Match, error) {
	now := time.Now().UTC()
	window := now.Add(15 * time.Minute)

	docs, err := fs.Collection("matches").
		Where("status", "==", "scheduled").
		Where("scheduledAt", ">=", now).
		Where("scheduledAt", "<=", window).
		Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}

	matches := make([]Match, 0, len(docs))
	for _, d := range docs {
		var m Match
		if err := d.DataTo(&m); err == nil {
			matches = append(matches, m)
		}
	}
	return matches, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Kubernetes — patch stream-service replica count
// ────────────────────────────────────────────────────────────────────────────

// patchReplicas patches a Deployment's replica count using in-cluster config.
// Works automatically when running as a Kubernetes CronJob pod.
// Locally this will fail with "in-cluster config" error — that is expected.
func patchReplicas(ctx context.Context, namespace, deployment string, replicas int32) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}

	patch := map[string]any{"spec": map[string]any{"replicas": replicas}}
	patchBytes, _ := json.Marshal(patch)

	_, err = clientset.AppsV1().
		Deployments(namespace).
		Patch(ctx, deployment, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

// ────────────────────────────────────────────────────────────────────────────
// Scale logic
// ────────────────────────────────────────────────────────────────────────────

func checkAndScale(ctx context.Context, fs *firestore.Client) {
	namespace := util.Getenv("K8S_NAMESPACE", "production")
	deployment := util.Getenv("STREAM_DEPLOYMENT", "stream-service")

	matches, err := getUpcomingMatches(ctx, fs)
	if err != nil {
		log.Printf(`{"service":"pre-scaler","level":"error","msg":"firestore query failed","error":%q}`, err.Error())
		return
	}

	if len(matches) > 0 {
		log.Printf(`{"service":"pre-scaler","level":"info","msg":"upcoming match found","count":%d}`, len(matches))

		// Try to scale Kubernetes — will fail locally (expected), works on GKE
		if err := patchReplicas(ctx, namespace, deployment, 20); err != nil {
			log.Printf(`{"service":"pre-scaler","level":"warn","msg":"k8s scale up failed (expected locally)","error":%q}`, err.Error())
		} else {
			log.Println(`{"service":"pre-scaler","level":"info","msg":"scaled up to 20 pods"}`)
		}

		// Publish match_reminder for each — Notification Service sends FCM push to Android
		// This WILL work locally since Pub/Sub is connected
		for _, m := range matches {
			if err := psclient.PublishEvent(ctx, "notification_events", map[string]any{
				"eventType": "match_reminder",
				"streamId":  m.ID,
				"title":     m.Title,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			}); err == nil {
				log.Printf(`{"service":"pre-scaler","level":"info","msg":"match_reminder published","matchId":%q,"title":%q}`, m.ID, m.Title)
			}
		}

	} else {
		log.Println(`{"service":"pre-scaler","level":"info","msg":"no upcoming matches — scaling down to 4"}`)

		// Try to scale down — will fail locally (expected), works on GKE
		if err := patchReplicas(ctx, namespace, deployment, 4); err != nil {
			log.Printf(`{"service":"pre-scaler","level":"warn","msg":"k8s scale down failed (expected locally)","error":%q}`, err.Error())
		} else {
			log.Println(`{"service":"pre-scaler","level":"info","msg":"scaled down to 4 pods"}`)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Main — runs once and exits (Kubernetes CronJob schedule: "*/1 * * * *")
// ────────────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")

	log.Printf(`{"service":"pre-scaler","level":"info","msg":"run starting","time":%q}`, time.Now().UTC().Format(time.RFC3339))

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

	// Pub/Sub — pass credentials explicitly
	if _, err := psclient.InitClient(ctx, projectID, credsFile); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}

	checkAndScale(ctx, fs)

	log.Println(`{"service":"pre-scaler","level":"info","msg":"run complete"}`)
	// Process exits — Kubernetes CronJob restarts it every minute
}
