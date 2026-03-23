package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
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

type Match struct {
	ID          string    `firestore:"id"`
	Title       string    `firestore:"title"`
	ScheduledAt time.Time `firestore:"scheduledAt"`
	Status      string    `firestore:"status"` // scheduled | live | ended
}

// ────────────────────────────────────────────────────────────────────────────
// Firestore queries
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

// getLiveViewers returns total viewers across all live streams.
func getLiveViewers(ctx context.Context, fs *firestore.Client) (int, error) {
	docs, err := fs.Collection("streams").
		Where("status", "==", "live").
		Documents(ctx).GetAll()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, d := range docs {
		var s struct {
			ViewerCount int `firestore:"viewerCount"`
		}
		if err := d.DataTo(&s); err == nil {
			total += s.ViewerCount
		}
	}
	return total, nil
}

// getNextScheduledMatch returns the very next match scheduled in the future.
func getNextScheduledMatch(ctx context.Context, fs *firestore.Client) (*Match, error) {
	docs, err := fs.Collection("matches").
		Where("status", "==", "scheduled").
		Where("scheduledAt", ">=", time.Now().UTC()).
		OrderBy("scheduledAt", firestore.Asc).
		Limit(1).
		Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, nil
	}
	var m Match
	docs[0].DataTo(&m)
	return &m, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Smart replica calculator — money-saving brain
// ────────────────────────────────────────────────────────────────────────────

const viewersPerPod = 500

func calculateOptimalReplicas(upcoming []Match, viewers int, next *Match) int32 {

	// ── Case 1: Match starting in 15 min → pre-warm ───────────────────────
	if len(upcoming) > 0 {
		expected := len(upcoming) * 2000 // 2000 viewers per match estimate
		pods := int(math.Ceil(float64(expected) / float64(viewersPerPod)))
		if pods < 4 {
			pods = 4
		}
		log.Printf(`{"service":"pre-scaler","msg":"pre-warming","matches":%d,"estimatedViewers":%d,"pods":%d}`,
			len(upcoming), expected, pods)
		return int32(pods)
	}

	// ── Case 2: Live viewers → scale to exact demand ───────────────────────
	if viewers > 0 {
		pods := int(math.Ceil(float64(viewers) / float64(viewersPerPod)))
		if pods < 2 {
			pods = 2 // minimum 2 pods for HA during live
		}
		log.Printf(`{"service":"pre-scaler","msg":"scale-to-demand","viewers":%d,"pods":%d}`,
			viewers, pods)
		return int32(pods)
	}

	// ── Case 3: Next match within 1 hour → keep 1 warm pod ────────────────
	if next != nil {
		hoursUntil := time.Until(next.ScheduledAt).Hours()
		if hoursUntil <= 1 {
			log.Printf(`{"service":"pre-scaler","msg":"standby","nextMatch":%q,"hoursUntil":%.1f,"pods":1}`,
				next.Title, hoursUntil)
			return 1
		}
	}

	// ── Case 4: Nothing happening → SCALE TO ZERO 💰 ──────────────────────
	log.Println(`{"service":"pre-scaler","msg":"scale-to-zero","cost":"$0"}`)
	return 0
}

// ────────────────────────────────────────────────────────────────────────────
// Kubernetes patch
// ────────────────────────────────────────────────────────────────────────────

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
	_, err = clientset.AppsV1().Deployments(namespace).
		Patch(ctx, deployment, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

// ────────────────────────────────────────────────────────────────────────────
// Main scaling logic
// ────────────────────────────────────────────────────────────────────────────

func checkAndScale(ctx context.Context, fs *firestore.Client) {
	namespace := util.Getenv("K8S_NAMESPACE", "production")
	deployment := util.Getenv("STREAM_DEPLOYMENT", "stream-service")

	upcoming, err := getUpcomingMatches(ctx, fs)
	if err != nil {
		log.Printf(`{"service":"pre-scaler","level":"error","msg":"query failed","error":%q}`, err.Error())
		return
	}

	viewers, err := getLiveViewers(ctx, fs)
	if err != nil {
		log.Printf(`{"service":"pre-scaler","level":"warn","msg":"viewer count failed","error":%q}`, err.Error())
	}

	next, err := getNextScheduledMatch(ctx, fs)
	if err != nil {
		log.Printf(`{"service":"pre-scaler","level":"warn","msg":"next match query failed","error":%q}`, err.Error())
	}

	replicas := calculateOptimalReplicas(upcoming, viewers, next)

	log.Printf(`{"service":"pre-scaler","level":"info","msg":"decision","replicas":%d,"viewers":%d,"upcomingMatches":%d}`,
		replicas, viewers, len(upcoming))

	if err := patchReplicas(ctx, namespace, deployment, replicas); err != nil {
		log.Printf(`{"service":"pre-scaler","level":"warn","msg":"k8s patch failed (expected locally)","error":%q}`, err.Error())
	} else {
		log.Printf(`{"service":"pre-scaler","level":"info","msg":"scaled","replicas":%d}`, replicas)
	}

	// Notify Android users about upcoming matches
	for _, m := range upcoming {
		psclient.PublishEvent(ctx, "notification_events", map[string]any{
			"eventType": "match_reminder",
			"streamId":  m.ID,
			"title":     m.Title,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
		log.Printf(`{"service":"pre-scaler","level":"info","msg":"reminder_sent","match":%q}`, m.Title)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")

	log.Printf(`{"service":"pre-scaler","level":"info","msg":"run starting","time":%q}`,
		time.Now().UTC().Format(time.RFC3339))

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

	if _, err := psclient.InitClient(ctx, projectID, credsFile); err != nil {
		log.Fatalf("pubsub init: %v", err)
	}

	checkAndScale(ctx, fs)

	log.Println(`{"service":"pre-scaler","level":"info","msg":"run complete"}`)
}
