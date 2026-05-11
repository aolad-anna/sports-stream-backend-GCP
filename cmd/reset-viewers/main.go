package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"sports-stream-backend/pkg/util"
)

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	bucket := util.Getenv("GCS_BUCKET", "sports-stream-66553.appspot.com")
	creds := util.Getenv("FIREBASE_CREDENTIALS", "")

	var fsOpts []option.ClientOption
	if strings.HasPrefix(strings.TrimSpace(creds), "{") {
		fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(creds)))
	} else if creds != "" {
		fsOpts = append(fsOpts, option.WithCredentialsFile(creds))
	}

	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("firestore init: %v", err)
	}
	defer fs.Close()

	var gcsOpts []option.ClientOption
	if strings.HasPrefix(strings.TrimSpace(creds), "{") {
		gcsOpts = append(gcsOpts, option.WithCredentialsJSON([]byte(creds)))
	} else if creds != "" {
		gcsOpts = append(gcsOpts, option.WithCredentialsFile(creds))
	}
	gcs, err := storage.NewClient(ctx, gcsOpts...)
	if err != nil {
		log.Fatalf("gcs init: %v", err)
	}
	defer gcs.Close()

	fmt.Println("=== Sports Stream Full Reset ===")
	fmt.Println()

	// ── 1. Reset streams/viewerCount + delete viewers subcollection ───────────
	fmt.Println("▶ Step 1: Resetting streams/viewerCount and clearing viewers subcollection...")
	streamDocs, err := fs.Collection("streams").Documents(ctx).GetAll()
	if err != nil {
		log.Fatalf("failed to fetch streams: %v", err)
	}

	streamReset, viewerDocsDeleted := 0, 0
	for _, doc := range streamDocs {
		data := doc.Data()
		id := doc.Ref.ID
		oldCount, _ := data["viewerCount"].(int64)
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

	analyticsReset, historyDocsDeleted := 0, 0
	for _, doc := range analyticsDocs {
		data := doc.Data()
		id := doc.Ref.ID
		oldCurrent, _ := data["currentViewers"].(int64)
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
		deleted := batchDeleteSubcollection(ctx, fs, doc.Ref.Collection("viewerHistory"))
		historyDocsDeleted += deleted
		if deleted > 0 {
			fmt.Printf("    └─ cleared %d viewerHistory docs\n", deleted)
		}
	}
	fmt.Printf("  Done: %d/%d analytics docs reset, %d history docs deleted\n\n",
		analyticsReset, len(analyticsDocs), historyDocsDeleted)

	// ── 3. Delete all video Firestore docs ────────────────────────────────────
	fmt.Println("▶ Step 3: Deleting all video records from Firestore...")
	videoDocs, err := fs.Collection("videos").Documents(ctx).GetAll()
	if err != nil {
		log.Fatalf("failed to fetch videos: %v", err)
	}
	videosDeleted := 0
	for _, doc := range videoDocs {
		if _, err := doc.Ref.Delete(ctx); err != nil {
			fmt.Printf("  ✗ video %s — delete error: %v\n", doc.Ref.ID, err)
			continue
		}
		fmt.Printf("  ✓ deleted video record: %s\n", doc.Ref.ID)
		videosDeleted++
	}
	fmt.Printf("  Done: %d video records deleted from Firestore\n\n", videosDeleted)

	// ── 4. Empty GCS bucket — uploads/ hls/ live/ folders ────────────────────
	fmt.Println("▶ Step 4: Emptying GCS bucket video folders...")
	fmt.Printf("  Bucket: gs://%s\n", bucket)

	prefixes := []string{"uploads/", "hls/", "live/", "videos/"}
	totalGCSDeleted := 0

	for _, prefix := range prefixes {
		count, err := deleteGCSPrefix(ctx, gcs, bucket, prefix)
		if err != nil {
			fmt.Printf("  ✗ error deleting prefix %s: %v\n", prefix, err)
			continue
		}
		fmt.Printf("  ✓ deleted %d objects under gs://%s/%s\n", count, bucket, prefix)
		totalGCSDeleted += count
	}
	fmt.Printf("  Done: %d GCS objects deleted\n\n", totalGCSDeleted)

	// ── Summary ───────────────────────────────────────────────────────────────
	fmt.Println("=== Reset Complete ===")
	fmt.Printf("  streams reset           : %d\n", streamReset)
	fmt.Printf("  viewer presence cleared : %d docs\n", viewerDocsDeleted)
	fmt.Printf("  analytics reset         : %d\n", analyticsReset)
	fmt.Printf("  viewer history cleared  : %d docs\n", historyDocsDeleted)
	fmt.Printf("  video Firestore records : %d deleted\n", videosDeleted)
	fmt.Printf("  GCS objects deleted     : %d\n", totalGCSDeleted)
	fmt.Println()
	fmt.Println("  Preserved (NOT touched):")
	fmt.Println("    peakViewers  — historical peak kept")
	fmt.Println("    totalJoins   — historical total kept")
	fmt.Println("    stream docs  — titles/status kept, only viewerCount reset")
}

// deleteGCSPrefix deletes all objects under a GCS prefix.
// Returns count of deleted objects.

func deleteGCSPrefix(ctx context.Context, gcs *storage.Client, bucket, prefix string) (int, error) {
	bkt := gcs.Bucket(bucket)
	it := bkt.Objects(ctx, &storage.Query{Prefix: prefix})
	count := 0

	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return count, fmt.Errorf("list objects: %w", err)
		}
		if err := bkt.Object(attrs.Name).Delete(ctx); err != nil {
			fmt.Printf("    ✗ failed to delete gs://%s/%s: %v\n", bucket, attrs.Name, err)
			continue
		}
		fmt.Printf("    - deleted gs://%s/%s\n", bucket, attrs.Name)
		count++
	}
	return count, nil
}

// batchDeleteSubcollection deletes all docs in a subcollection in batches of 500.

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
		if (i+1)%500 == 0 {
			if _, err := batch.Commit(ctx); err != nil {
				log.Printf("batch commit error: %v", err)
			}
			batch = fs.Batch()
		}
	}
	if deleted%500 != 0 {
		if _, err := batch.Commit(ctx); err != nil {
			log.Printf("batch commit error: %v", err)
		}
	}
	return deleted
}
