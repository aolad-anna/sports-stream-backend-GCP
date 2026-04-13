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

	fmt.Println("=== Sports Stream Full Reset ===")
	fmt.Println()

	// ── 1. Reset streams/viewerCount + delete viewers subcollection ───────────
	fmt.Println("▶ Step 1: Resetting streams/viewerCount and clearing viewers subcollection...")
	streamDocs, err := fs.Collection("streams").Documents(ctx).GetAll()
	if err != nil {
		log.Fatalf("failed to fetch streams: %v", err)
	}

	streamReset := 0
	viewerDocsDeleted := 0

	for _, doc := range streamDocs {
		data := doc.Data()
		id := doc.Ref.ID
		oldCount, _ := data["viewerCount"].(int64)

		// Reset viewerCount
		_, err := doc.Ref.Update(ctx, []firestore.Update{
			{Path: "viewerCount", Value: int64(0)},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
		if err != nil {
			fmt.Printf("  ✗ stream %s — update error: %v\n", id, err)
			continue
		}
		fmt.Printf("  ✓ stream %-35s viewerCount: %d → 0\n", id, oldCount)
		streamReset++

		// Batch-delete streams/{id}/viewers/* subcollection
		deleted := batchDeleteSubcollection(ctx, fs, doc.Ref.Collection("viewers"))
		viewerDocsDeleted += deleted
		if deleted > 0 {
			fmt.Printf("    └─ cleared %d viewer presence docs\n", deleted)
		}
	}
	fmt.Printf("  Done: %d/%d streams reset, %d viewer docs deleted\n\n",
		streamReset, len(streamDocs), viewerDocsDeleted)

	// ── 2. Reset analytics/currentViewers + delete viewerHistory subcollection ─
	fmt.Println("▶ Step 2: Resetting analytics/currentViewers and clearing viewerHistory...")
	analyticsDocs, err := fs.Collection("analytics").Documents(ctx).GetAll()
	if err != nil {
		log.Fatalf("failed to fetch analytics: %v", err)
	}

	analyticsReset := 0
	historyDocsDeleted := 0

	for _, doc := range analyticsDocs {
		data := doc.Data()
		id := doc.Ref.ID
		oldCurrent, _ := data["currentViewers"].(int64)

		// Reset currentViewers only — preserve peakViewers and totalJoins
		_, err := doc.Ref.Update(ctx, []firestore.Update{
			{Path: "currentViewers", Value: int64(0)},
			{Path: "updatedAt", Value: time.Now().UTC()},
		})
		if err != nil {
			fmt.Printf("  ✗ analytics %s — update error: %v\n", id, err)
			continue
		}
		fmt.Printf("  ✓ analytics %-35s currentViewers: %d → 0\n", id, oldCurrent)
		analyticsReset++

		// Batch-delete analytics/{id}/viewerHistory/* subcollection
		deleted := batchDeleteSubcollection(ctx, fs, doc.Ref.Collection("viewerHistory"))
		historyDocsDeleted += deleted
		if deleted > 0 {
			fmt.Printf("    └─ cleared %d viewerHistory docs\n", deleted)
		}
	}
	fmt.Printf("  Done: %d/%d analytics docs reset, %d history docs deleted\n\n",
		analyticsReset, len(analyticsDocs), historyDocsDeleted)

	// ── Summary ───────────────────────────────────────────────────────────────
	fmt.Println("=== Reset Complete ===")
	fmt.Printf("  streams reset          : %d\n", streamReset)
	fmt.Printf("  viewer presence cleared: %d docs\n", viewerDocsDeleted)
	fmt.Printf("  analytics reset        : %d\n", analyticsReset)
	fmt.Printf("  viewer history cleared : %d docs\n", historyDocsDeleted)
	fmt.Println()
	fmt.Println("  Preserved (NOT touched):")
	fmt.Println("    peakViewers  — historical peak kept")
	fmt.Println("    totalJoins   — historical total kept")
}

// batchDeleteSubcollection deletes all docs in a subcollection in batches of 500.
// Returns the number of docs deleted.

func batchDeleteSubcollection(ctx context.Context, fs *firestore.Client, col *firestore.CollectionRef) int {
	docs, err := col.Documents(ctx).GetAll()
	if err != nil || len(docs) == 0 {
		return 0
	}

	deleted := 0
	batch := fs.Batch()

	for i, d := range docs {
		batch.Delete(d.Ref)
		deleted++
		// Commit every 500 — Firestore batch limit
		if (i+1)%500 == 0 {
			if _, err := batch.Commit(ctx); err != nil {
				log.Printf("batch commit error: %v", err)
			}
			batch = fs.Batch()
		}
	}

	// Commit remaining
	if deleted%500 != 0 {
		if _, err := batch.Commit(ctx); err != nil {
			log.Printf("batch commit error: %v", err)
		}
	}

	return deleted
}
