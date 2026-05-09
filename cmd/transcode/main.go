// cmd/transcode/main.go
// ── CLI tool to manually trigger transcoding for a video ──────────────────────
// Usage:
//   go run cmd/transcode/main.go -video <videoId>
//   go run cmd/transcode/main.go -gcs gs://bucket/path/video.mp4 -out gs://bucket/hls/test/
//
// This is useful for:
//   - Re-triggering a failed transcode job
//   - Testing the Transcoder API directly
//   - Manually transcoding a video that already exists in GCS

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"sports-stream-backend/pkg/util"

	"golang.org/x/oauth2/google"
)

func main() {
	videoID := flag.String("video", "", "Firestore video ID to retranscode")
	inputURI := flag.String("input", "", "GCS input URI (e.g. gs://bucket/uploads/uid/video.mp4)")
	outputURI := flag.String("output", "", "GCS output URI (e.g. gs://bucket/hls/videoId/)")
	dryRun := flag.Bool("dry-run", false, "Print job payload without submitting")
	flag.Parse()

	ctx := context.Background()

	projectID := util.MustGetenv("GCP_PROJECT_ID")
	bucket := util.Getenv("GCS_BUCKET", "sports-stream-66553.appspot.com")
	location := util.Getenv("TRANSCODER_LOCATION", "europe-west1")

	// Resolve input/output URIs
	var inURI, outURI string
	if *inputURI != "" {
		inURI = *inputURI
		outURI = *outputURI
		if outURI == "" {
			log.Fatal("❌ -output required when using -input")
		}
	} else if *videoID != "" {
		// Use video ID to construct paths
		inURI = fmt.Sprintf("gs://%s/uploads/%s.mp4", bucket, *videoID)
		outURI = fmt.Sprintf("gs://%s/hls/%s/", bucket, *videoID)
	} else {
		fmt.Println("Usage:")
		fmt.Println("  go run cmd/transcode/main.go -video <videoId>")
		fmt.Println("  go run cmd/transcode/main.go -input gs://bucket/file.mp4 -output gs://bucket/hls/out/")
		fmt.Println("")
		fmt.Println("Flags:")
		flag.PrintDefaults()
		log.Fatal("❌ Either -video or -input/-output required")
	}

	// Get OAuth2 token
	fmt.Println("🔑 Getting auth token...")
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		log.Fatalf("❌ Token source: %v", err)
	}
	tok, err := ts.Token()
	if err != nil {
		log.Fatalf("❌ Get token: %v", err)
	}
	authToken := tok.AccessToken
	fmt.Println("✅ Auth token obtained")

	// Verify input file exists
	fmt.Printf("🔍 Checking input file: %s\n", inURI)
	gcsPath := strings.TrimPrefix(inURI, fmt.Sprintf("gs://%s/", bucket))
	checkURL := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s",
		bucket, strings.ReplaceAll(gcsPath, "/", "%2F"))
	checkReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	checkReq.Header.Set("Authorization", "Bearer "+authToken)
	checkResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(checkReq)
	if err != nil {
		log.Fatalf("❌ GCS check failed: %v", err)
	}
	checkResp.Body.Close()
	if checkResp.StatusCode == 404 {
		log.Fatalf("❌ Input file NOT found in GCS: %s", inURI)
	}
	fmt.Printf("✅ Input file found in GCS\n")

	// Build job payload
	submitURL := fmt.Sprintf(
		"https://transcoder.googleapis.com/v1/projects/%s/locations/%s/jobs",
		projectID, location)

	jobPayload := map[string]any{
		"inputUri": inURI, "outputUri": outURI,
		"elementaryStreams": []map[string]any{
			{"key": "video-720p", "videoStream": map[string]any{"h264": map[string]any{
				"heightPixels": 720, "widthPixels": 1280, "bitrateBps": 1500000, "frameRate": 30}}},
			{"key": "video-480p", "videoStream": map[string]any{"h264": map[string]any{
				"heightPixels": 480, "widthPixels": 854, "bitrateBps": 800000, "frameRate": 30}}},
			{"key": "video-360p", "videoStream": map[string]any{"h264": map[string]any{
				"heightPixels": 360, "widthPixels": 640, "bitrateBps": 400000, "frameRate": 30}}},
			{"key": "audio", "audioStream": map[string]any{"codec": "aac", "bitrateBps": 128000}},
		},
		"muxStreams": []map[string]any{
			{"key": "hls-720p", "container": "ts",
				"elementaryStreams": []string{"video-720p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "6s"}},
			{"key": "hls-480p", "container": "ts",
				"elementaryStreams": []string{"video-480p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "6s"}},
			{"key": "hls-360p", "container": "ts",
				"elementaryStreams": []string{"video-360p", "audio"},
				"segmentSettings":   map[string]any{"segmentDuration": "6s"}},
		},
		"manifests": []map[string]any{
			{"fileName": "index.m3u8", "type": "HLS",
				"muxStreams": []string{"hls-720p", "hls-480p", "hls-360p"}},
		},
	}

	body, _ := json.MarshalIndent(map[string]any{"job": jobPayload}, "", "  ")

	if *dryRun {
		fmt.Println("\n📋 Dry run — job payload:")
		fmt.Println(string(body))
		fmt.Println("\n✅ Dry run complete — no job submitted")
		return
	}

	// Submit job
	fmt.Printf("\n🚀 Submitting transcoder job...\n")
	fmt.Printf("   Input:  %s\n", inURI)
	fmt.Printf("   Output: %s\n", outURI)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, submitURL,
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("❌ API call failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		log.Fatalf("❌ Transcoder API error %d:\n%s", resp.StatusCode, string(respBody))
	}

	var jobResp map[string]any
	json.Unmarshal(respBody, &jobResp)
	jobName, _ := jobResp["name"].(string)

	fmt.Printf("✅ Job submitted: %s\n", jobName)
	fmt.Printf("\n⏳ Polling job status (every 10s)...\n")

	jobURL := fmt.Sprintf("https://transcoder.googleapis.com/v1/%s", jobName)

	for i := 1; i <= 90; i++ {
		time.Sleep(10 * time.Second)

		pollTok, _ := ts.Token()
		pollReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, jobURL, nil)
		pollReq.Header.Set("Authorization", "Bearer "+pollTok.AccessToken)
		pollResp, err := client.Do(pollReq)
		if err != nil {
			fmt.Printf("  [%d] Poll error: %v\n", i, err)
			continue
		}
		var result map[string]any
		json.NewDecoder(pollResp.Body).Decode(&result)
		pollResp.Body.Close()

		state, _ := result["state"].(string)
		fmt.Printf("  [%d] State: %s\n", i, state)

		if state == "SUCCEEDED" {
			hlsURL := fmt.Sprintf("https://storage.googleapis.com/%s/hls/%s/index.m3u8",
				bucket, *videoID)
			fmt.Println("\n✅ TRANSCODING COMPLETE!")
			fmt.Printf("   HLS URL: %s\n", hlsURL)
			return
		} else if state == "FAILED" {
			errDetail, _ := json.MarshalIndent(result["error"], "", "  ")
			log.Fatalf("\n❌ TRANSCODING FAILED:\n%s", string(errDetail))
		}
	}

	log.Fatal("❌ Timed out waiting for transcoding")
}
