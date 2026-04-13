package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"

	"sports-stream-backend/pkg/util"
)

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	credsValue := util.Getenv("FIREBASE_CREDENTIALS", "")

	// Support both JSON string and file path
	var fsOpts []option.ClientOption
	if strings.HasPrefix(strings.TrimSpace(credsValue), "{") {
		fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(credsValue)))
	} else if credsValue != "" {
		fsOpts = append(fsOpts, option.WithCredentialsFile(credsValue))
	}

	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("firestore init: %v", err)
	}
	defer fs.Close()

	fmt.Println("=== Sports Stream Viewer Reset ===")
	fmt.Println()

	// ── Reset streams collection ──────────────────────────────────────────
	fmt.Println("▶ Resetting streams/viewerCount ...")
	streamDocs, err := fs.Collection("streams").Documents(ctx).GetAll()
	if err != nil {
		log.Fatalf("failed to fetch streams: %v", err)
	}

	streamReset := 0
	for _, doc := range streamDocs {
		data := doc.Data()
		id := doc.Ref.ID
		oldCount, _ := data["viewerCount"].(int64)

		_, err := doc.Ref.Update(ctx, []firestore.Update{
			{Path: "viewerCount", Value: 0},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
		if err != nil {
			fmt.Printf("  ✗ stream %s — error: %v\n", id, err)
			continue
		}
		fmt.Printf("  ✓ stream %-30s  viewerCount: %d → 0\n", id, oldCount)
		streamReset++
	}
	fmt.Printf("  Done: %d/%d streams reset\n\n", streamReset, len(streamDocs))

	// ── Reset analytics collection ────────────────────────────────────────
	fmt.Println("▶ Resetting analytics/currentViewers ...")
	analyticsDocs, err := fs.Collection("analytics").Documents(ctx).GetAll()
	if err != nil {
		log.Fatalf("failed to fetch analytics: %v", err)
	}

	analyticsReset := 0
	for _, doc := range analyticsDocs {
		data := doc.Data()
		id := doc.Ref.ID
		oldCurrent, _ := data["currentViewers"].(int64)

		_, err := doc.Ref.Update(ctx, []firestore.Update{
			{Path: "currentViewers", Value: int64(0)},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
		if err != nil {
			fmt.Printf("  ✗ analytics %s — error: %v\n", id, err)
			continue
		}
		fmt.Printf("  ✓ analytics %-30s  currentViewers: %d → 0\n", id, oldCurrent)
		analyticsReset++
	}
	fmt.Printf("  Done: %d/%d analytics docs reset\n\n", analyticsReset, len(analyticsDocs))

	// ── Summary ───────────────────────────────────────────────────────────
	fmt.Println("=== Reset Complete ===")
	fmt.Printf("  streams reset:   %d\n", streamReset)
	fmt.Printf("  analytics reset: %d\n", analyticsReset)
	fmt.Println()
	fmt.Println("Note: peakViewers and totalJoins are NOT touched — historical data preserved.")
}
