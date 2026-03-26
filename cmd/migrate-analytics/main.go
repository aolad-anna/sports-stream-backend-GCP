package main

import (
	"context"
	"log"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"sports-stream-backend/pkg/util"
)

type StreamStats struct {
	StreamID       string    `firestore:"streamId"`
	CurrentViewers int       `firestore:"currentViewers"`
	PeakViewers    int       `firestore:"peakViewers"`
	TotalJoins     int       `firestore:"totalJoins"`
	UpdatedAt      time.Time `firestore:"updatedAt"`
}

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	credsFile := util.Getenv("FIREBASE_CREDENTIALS", "")

	var fsOpts []option.ClientOption
	if credsFile != "" {
		fsOpts = append(fsOpts, option.WithCredentialsFile(credsFile))
	}

	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("firestore init: %v", err)
	}
	defer fs.Close()

	// 1. Get ALL streams (not just live — include ended ones)
	docs, err := fs.Collection("streams").Documents(ctx).GetAll()
	if err != nil {
		log.Fatalf("failed to fetch streams: %v", err)
	}

	log.Printf("Found %d streams — checking analytics...", len(docs))

	created := 0
	skipped := 0

	for _, doc := range docs {
		streamID := doc.Ref.ID

		// 2. Check if analytics doc already exists
		analyticsRef := fs.Collection("analytics").Doc(streamID)
		_, err := analyticsRef.Get(ctx)

		if status.Code(err) == codes.NotFound {
			// 3. Create missing analytics doc
			stats := StreamStats{
				StreamID:       streamID,
				CurrentViewers: 0,
				PeakViewers:    0,
				TotalJoins:     0,
				UpdatedAt:      time.Now().UTC(),
			}
			if _, err := analyticsRef.Set(ctx, stats); err != nil {
				log.Printf("  ✗ FAILED to create analytics for %s: %v", streamID, err)
			} else {
				log.Printf("  ✓ Created analytics for %s", streamID)
				created++
			}
		} else if err != nil {
			log.Printf("  ✗ Error checking analytics for %s: %v", streamID, err)
		} else {
			log.Printf("  · Skipped %s (already exists)", streamID)
			skipped++
		}
	}

	log.Printf("\nDone! Created: %d  Skipped: %d  Total: %d", created, skipped, len(docs))
}
