package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gorilla/mux"

	"sports-stream-backend/pkg/middleware"
	"sports-stream-backend/pkg/util"
)

type ChatMessage struct {
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	Timestamp int64  `json:"timestamp"`
	Role      string `json:"role"`
}

type SendChatRequest struct {
	Text string `json:"text"`
}

// ── POST /api/v1/streams/{id}/chat ───────────────────────────────────────────

func (h *handler) sendChatMessage(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	uid, _ := middleware.UIDFromContext(r.Context())

	var req SendChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	text := strings.TrimSpace(req.Text)
	if text == "" {
		jsonError(w, "message cannot be empty", http.StatusBadRequest)
		return
	}
	if len([]rune(text)) > 200 {
		jsonError(w, "message too long (max 200 chars)", http.StatusBadRequest)
		return
	}

	snap, err := h.fs.Collection("streams").Doc(streamID).Get(r.Context())
	if err != nil {
		jsonError(w, "stream not found", http.StatusNotFound)
		return
	}
	var s Stream
	snap.DataTo(&s)
	if s.Status != "live" {
		jsonError(w, "stream is not live", http.StatusGone)
		return
	}

	name := getSenderName(r.Context(), h.fs, uid)
	role := h.getUserRole(r.Context(), uid)

	msg := ChatMessage{
		UID:       uid,
		Name:      name,
		Text:      text,
		Timestamp: time.Now().UnixMilli(),
		Role:      role,
	}

	msgID, err := writeToRealtimeDB(r.Context(), streamID, msg)
	if err != nil {
		log.Printf("chat: realtime DB write failed streamId=%s: %v", streamID, err)
		jsonError(w, "failed to send message", http.StatusInternalServerError)
		return
	}

	log.Printf("chat: message sent streamId=%s uid=%s msgId=%s", streamID, uid, msgID)
	jsonOK(w, map[string]any{
		"msgId":     msgID,
		"streamId":  streamID,
		"timestamp": msg.Timestamp,
	})
}

// ── GET /api/v1/streams/{id}/chat ────────────────────────────────────────────

func (h *handler) getChatHistory(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]

	messages, err := readChatHistory(r.Context(), streamID, 50)
	if err != nil {
		log.Printf("chat: history read failed streamId=%s: %v", streamID, err)
		jsonError(w, "failed to fetch chat history", http.StatusInternalServerError)
		return
	}

	jsonOK(w, messages)
}

// ── DELETE /api/v1/streams/{id}/chat/{msgId} ─────────────────────────────────

func (h *handler) deleteChatMessage(w http.ResponseWriter, r *http.Request) {
	streamID := mux.Vars(r)["id"]
	msgID := mux.Vars(r)["msgId"]
	uid, _ := middleware.UIDFromContext(r.Context())
	role := h.getUserRole(r.Context(), uid)

	if role != "admin" && role != "broadcaster" {
		jsonError(w, "only admins and broadcasters can delete messages", http.StatusForbidden)
		return
	}

	if err := deleteChatMsg(r.Context(), streamID, msgID); err != nil {
		jsonError(w, "failed to delete message", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{"deleted": true, "msgId": msgID})
}

// ── Realtime DB helpers ───────────────────────────────────────────────────────
// Uses RTDB_SECRET env var — avoids OAuth2 scope issues entirely.
// Get secret: Firebase Console → Project Settings → Service Accounts → Database Secrets

func rtdbEndpoint(path string) string {
	dbURL := util.Getenv("RTDB_URL", "https://sports-stream-66553-default-rtdb.europe-west1.firebasedatabase.app")
	secret := os.Getenv("RTDB_SECRET")
	if secret != "" {
		return fmt.Sprintf("%s/%s.json?auth=%s", dbURL, path, secret)
	}
	return fmt.Sprintf("%s/%s.json", dbURL, path)
}

func writeToRealtimeDB(ctx context.Context, streamID string, msg ChatMessage) (string, error) {
	endpoint := rtdbEndpoint(fmt.Sprintf("chats/%s/messages", streamID))

	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("realtime DB %d: %s", resp.StatusCode, string(b))
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	msgID, ok := result["name"]
	if !ok || msgID == "" {
		return "", fmt.Errorf("no push ID in response")
	}
	return msgID, nil
}

func readChatHistory(ctx context.Context, streamID string, limit int) ([]map[string]any, error) {
	dbURL := util.Getenv("RTDB_URL", "https://sports-stream-66553-default-rtdb.europe-west1.firebasedatabase.app")
	secret := os.Getenv("RTDB_SECRET")
	endpoint := fmt.Sprintf(
		"%s/chats/%s/messages.json?orderBy=\"timestamp\"&limitToLast=%d&auth=%s",
		dbURL, streamID, limit, secret,
	)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw map[string]map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	messages := make([]map[string]any, 0, len(raw))
	for msgID, msg := range raw {
		msg["msgId"] = msgID
		messages = append(messages, msg)
	}
	return messages, nil
}

func deleteChatMsg(ctx context.Context, streamID, msgID string) error {
	endpoint := rtdbEndpoint(fmt.Sprintf("chats/%s/messages/%s", streamID, msgID))
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func getSenderName(ctx context.Context, fs *firestore.Client, uid string) string {
	snap, err := fs.Collection("users").Doc(uid).Get(ctx)
	if err != nil {
		return "Anonymous"
	}
	var u struct {
		DisplayName string `firestore:"displayName"`
		Name        string `firestore:"name"`
	}
	snap.DataTo(&u)
	if u.DisplayName != "" {
		return u.DisplayName
	}
	if u.Name != "" {
		return u.Name
	}
	return "User"
}
