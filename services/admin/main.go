package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gorilla/mux"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	fbclient "sports-stream-backend/pkg/firebase"
	"sports-stream-backend/pkg/util"
)

type UserProfile struct {
	UID         string    `firestore:"uid" json:"uid"`
	Email       string    `firestore:"email" json:"email"`
	DisplayName string    `firestore:"displayName" json:"displayName"`
	Role        string    `firestore:"role" json:"role"`
	CreatedAt   time.Time `firestore:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time `firestore:"updatedAt" json:"updatedAt"`
}

type Stream struct {
	ID             string    `firestore:"id" json:"id"`
	Title          string    `firestore:"title" json:"title"`
	Status         string    `firestore:"status" json:"status"`
	HLSUrl         string    `firestore:"hlsUrl" json:"hlsUrl"`
	ViewerCount    int64     `firestore:"viewerCount" json:"viewerCount"`
	BroadcasterUID string    `firestore:"broadcasterUid" json:"broadcasterUid"`
	CreatedAt      time.Time `firestore:"createdAt" json:"createdAt"`
	UpdatedAt      time.Time `firestore:"updatedAt" json:"updatedAt"`
}

type AnalyticsDoc struct {
	StreamID       string    `firestore:"streamId" json:"streamId"`
	CurrentViewers int64     `firestore:"currentViewers" json:"currentViewers"`
	PeakViewers    int64     `firestore:"peakViewers" json:"peakViewers"`
	TotalJoins     int64     `firestore:"totalJoins" json:"totalJoins"`
	UpdatedAt      time.Time `firestore:"updatedAt" json:"updatedAt"`
}

type Match struct {
	ID          string    `firestore:"id" json:"id"`
	Title       string    `firestore:"title" json:"title"`
	ScheduledAt time.Time `firestore:"scheduledAt" json:"scheduledAt"`
	Status      string    `firestore:"status" json:"status"`
	CreatedAt   time.Time `firestore:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time `firestore:"updatedAt" json:"updatedAt"`
}

type DashboardStats struct {
	UsersTotal     int64 `json:"usersTotal"`
	AdminsTotal    int64 `json:"adminsTotal"`
	Broadcasters   int64 `json:"broadcasters"`
	Viewers        int64 `json:"viewers"`
	LiveStreams    int64 `json:"liveStreams"`
	CurrentViewers int64 `json:"currentViewers"`
	TotalJoinsAll  int64 `json:"totalJoinsAll"`
}

type handler struct {
	fs            *firestore.Client
	panelUser     string
	panelPassword string
	sessionSecret string
	cookieSecure  bool
}

func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > 500 {
		return 500
	}
	return n
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
}

func (h *handler) getUserRole(ctx context.Context, uid string) string {
	snap, err := h.fs.Collection("users").Doc(uid).Get(ctx)
	if err != nil {
		return ""
	}
	var profile struct {
		Role string `firestore:"role"`
	}
	if err := snap.DataTo(&profile); err != nil {
		return ""
	}
	return profile.Role
}

func (h *handler) isSessionAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("admin_session")
	if err != nil || cookie.Value == "" {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return false
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 3 || parts[0] != h.panelUser {
		return false
	}
	expUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().UTC().Unix() > expUnix {
		return false
	}
	payload := parts[0] + "|" + parts[1]
	mac := hmac.New(sha256.New, []byte(h.sessionSecret))
	_, _ = mac.Write([]byte(payload))
	expected := fmt.Sprintf("%x", mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(parts[2]))
}

func (h *handler) newSessionCookie() *http.Cookie {
	exp := time.Now().UTC().Add(12 * time.Hour).Unix()
	payload := fmt.Sprintf("%s|%d", h.panelUser, exp)
	mac := hmac.New(sha256.New, []byte(h.sessionSecret))
	_, _ = mac.Write([]byte(payload))
	sig := fmt.Sprintf("%x", mac.Sum(nil))
	token := base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + sig))
	return &http.Cookie{Name: "admin_session", Value: token, Path: "/", HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode, Expires: time.Now().UTC().Add(12 * time.Hour)}
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return h[7:]
	}
	return ""
}

func (h *handler) isFirebaseAdmin(r *http.Request) bool {
	token := extractBearer(r)
	if token == "" {
		return false
	}
	verified, err := fbclient.VerifyIDToken(r.Context(), token)
	if err != nil {
		return false
	}
	return h.getUserRole(r.Context(), verified.UID) == "admin"
}

func (h *handler) adminAuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.isSessionAuthenticated(r) && !h.isFirebaseAdmin(r) {
			jsonError(w, "admin access required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) uiSessionRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.isSessionAuthenticated(r) {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(loginHTML()))
}

func (h *handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Username != h.panelUser || body.Password != h.panelPassword {
		jsonError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, h.newSessionCookie())
	jsonOK(w, map[string]any{"loggedIn": true})
}

func (h *handler) logoutSubmit(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "admin_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: time.Unix(0, 0), MaxAge: -1})
	jsonOK(w, map[string]any{"loggedOut": true})
}

func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	stats := DashboardStats{}
	usersIter := h.fs.Collection("users").Documents(r.Context())
	for {
		doc, err := usersIter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			jsonError(w, "failed to scan users", http.StatusInternalServerError)
			return
		}
		stats.UsersTotal++
		var u UserProfile
		if err := doc.DataTo(&u); err == nil {
			switch u.Role {
			case "admin":
				stats.AdminsTotal++
			case "broadcaster":
				stats.Broadcasters++
			default:
				stats.Viewers++
			}
		}
	}

	streamsIter := h.fs.Collection("streams").Documents(r.Context())
	for {
		doc, err := streamsIter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			jsonError(w, "failed to scan streams", http.StatusInternalServerError)
			return
		}
		var s Stream
		if err := doc.DataTo(&s); err == nil && s.Status == "live" {
			stats.LiveStreams++
			stats.CurrentViewers += s.ViewerCount
		}
	}

	analyticsIter := h.fs.Collection("analytics").Documents(r.Context())
	for {
		doc, err := analyticsIter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			jsonError(w, "failed to scan analytics", http.StatusInternalServerError)
			return
		}
		var a AnalyticsDoc
		if err := doc.DataTo(&a); err == nil {
			stats.TotalJoinsAll += a.TotalJoins
		}
	}
	jsonOK(w, stats)
}

func listDocs[T any](ctx context.Context, q firestore.Query) ([]T, error) {
	iter := q.Documents(ctx)
	items := make([]T, 0)
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var it T
		if err := doc.DataTo(&it); err == nil {
			items = append(items, it)
		}
	}
	return items, nil
}

func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := listDocs[UserProfile](r.Context(), h.fs.Collection("users").Limit(parseLimit(r.URL.Query().Get("limit"), 100)))
	if err != nil {
		jsonError(w, "failed to list users", http.StatusInternalServerError)
		return
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UpdatedAt.After(users[j].UpdatedAt) })
	jsonOK(w, users)
}

func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	uid, _ := body["uid"].(string)
	email, _ := body["email"].(string)
	if strings.TrimSpace(uid) == "" || strings.TrimSpace(email) == "" {
		jsonError(w, "uid and email are required", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	body["uid"] = strings.TrimSpace(uid)
	body["email"] = strings.TrimSpace(email)
	if role, _ := body["role"].(string); strings.TrimSpace(role) == "" {
		body["role"] = "viewer"
	}
	body["createdAt"] = now
	body["updatedAt"] = now
	if _, err := h.fs.Collection("users").Doc(uid).Set(r.Context(), body); err != nil {
		jsonError(w, "failed to create user", http.StatusInternalServerError)
		return
	}
	jsonOK(w, body)
}

func (h *handler) getUser(w http.ResponseWriter, r *http.Request) {
	uid := mux.Vars(r)["uid"]
	snap, err := h.fs.Collection("users").Doc(uid).Get(r.Context())
	if err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}
	var u UserProfile
	if err := snap.DataTo(&u); err != nil {
		jsonError(w, "decode error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, u)
}

func (h *handler) updateUser(w http.ResponseWriter, r *http.Request) {
	uid := mux.Vars(r)["uid"]
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	updates := []firestore.Update{{Path: "updatedAt", Value: time.Now().UTC()}}
	for k, v := range body {
		updates = append(updates, firestore.Update{Path: k, Value: v})
	}
	if _, err := h.fs.Collection("users").Doc(uid).Update(r.Context(), updates); err != nil {
		jsonError(w, "failed to update user", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"uid": uid, "updated": true})
}

func (h *handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	uid := mux.Vars(r)["uid"]
	if _, err := h.fs.Collection("users").Doc(uid).Delete(r.Context()); err != nil {
		jsonError(w, "failed to delete user", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"uid": uid, "deleted": true})
}

func (h *handler) updateUserRole(w http.ResponseWriter, r *http.Request) {
	uid := mux.Vars(r)["uid"]
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if _, err := h.fs.Collection("users").Doc(uid).Update(r.Context(), []firestore.Update{{Path: "role", Value: strings.TrimSpace(body.Role)}, {Path: "updatedAt", Value: time.Now().UTC()}}); err != nil {
		jsonError(w, "failed to update role", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"uid": uid, "updated": true})
}

func (h *handler) listStreams(w http.ResponseWriter, r *http.Request) {
	streams, err := listDocs[Stream](r.Context(), h.fs.Collection("streams").Limit(parseLimit(r.URL.Query().Get("limit"), 100)))
	if err != nil {
		jsonError(w, "failed to list streams", http.StatusInternalServerError)
		return
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].UpdatedAt.After(streams[j].UpdatedAt) })
	jsonOK(w, streams)
}

func (h *handler) createStream(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	title, _ := body["title"].(string)
	if strings.TrimSpace(title) == "" {
		jsonError(w, "title is required", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("stream_%d", now.UnixMilli())
	body["id"] = id
	body["createdAt"] = now
	body["updatedAt"] = now
	if _, err := h.fs.Collection("streams").Doc(id).Set(r.Context(), body); err != nil {
		jsonError(w, "failed to create stream", http.StatusInternalServerError)
		return
	}
	jsonOK(w, body)
}

func (h *handler) getStream(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("streams").Doc(id).Get(r.Context())
	if err != nil {
		jsonError(w, "stream not found", http.StatusNotFound)
		return
	}
	var s Stream
	if err := snap.DataTo(&s); err != nil {
		jsonError(w, "decode error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, s)
}

func (h *handler) updateStream(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	updates := []firestore.Update{{Path: "updatedAt", Value: time.Now().UTC()}}
	for k, v := range body {
		updates = append(updates, firestore.Update{Path: k, Value: v})
	}
	if _, err := h.fs.Collection("streams").Doc(id).Update(r.Context(), updates); err != nil {
		jsonError(w, "failed to update stream", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"streamId": id, "updated": true})
}

func (h *handler) deleteStream(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, err := h.fs.Collection("streams").Doc(id).Delete(r.Context()); err != nil {
		jsonError(w, "failed to delete stream", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"streamId": id, "deleted": true})
}

func (h *handler) endStream(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, err := h.fs.Collection("streams").Doc(id).Update(r.Context(), []firestore.Update{{Path: "status", Value: "ended"}, {Path: "viewerCount", Value: 0}, {Path: "updatedAt", Value: time.Now().UTC()}}); err != nil {
		jsonError(w, "failed to end stream", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"streamId": id, "ended": true})
}

func (h *handler) resetStreamViewers(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, err := h.fs.Collection("streams").Doc(id).Update(r.Context(), []firestore.Update{{Path: "viewerCount", Value: 0}, {Path: "updatedAt", Value: time.Now().UTC()}}); err != nil {
		jsonError(w, "failed to reset viewers", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"streamId": id, "reset": true})
}

func (h *handler) listAnalytics(w http.ResponseWriter, r *http.Request) {
	docs, err := listDocs[AnalyticsDoc](r.Context(), h.fs.Collection("analytics").Limit(parseLimit(r.URL.Query().Get("limit"), 100)))
	if err != nil {
		jsonError(w, "failed to list analytics", http.StatusInternalServerError)
		return
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].UpdatedAt.After(docs[j].UpdatedAt) })
	jsonOK(w, docs)
}

func (h *handler) createAnalytics(w http.ResponseWriter, r *http.Request) {
	var body AnalyticsDoc
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.StreamID) == "" {
		jsonError(w, "streamId is required", http.StatusBadRequest)
		return
	}
	body.UpdatedAt = time.Now().UTC()
	if _, err := h.fs.Collection("analytics").Doc(body.StreamID).Set(r.Context(), body); err != nil {
		jsonError(w, "failed to create analytics", http.StatusInternalServerError)
		return
	}
	jsonOK(w, body)
}

func (h *handler) getAnalytics(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["streamId"]
	snap, err := h.fs.Collection("analytics").Doc(id).Get(r.Context())
	if err != nil {
		jsonError(w, "analytics not found", http.StatusNotFound)
		return
	}
	var a AnalyticsDoc
	if err := snap.DataTo(&a); err != nil {
		jsonError(w, "decode error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, a)
}

func (h *handler) updateAnalytics(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["streamId"]
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	updates := []firestore.Update{{Path: "updatedAt", Value: time.Now().UTC()}}
	for k, v := range body {
		updates = append(updates, firestore.Update{Path: k, Value: v})
	}
	if _, err := h.fs.Collection("analytics").Doc(id).Update(r.Context(), updates); err != nil {
		jsonError(w, "failed to update analytics", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"streamId": id, "updated": true})
}

func (h *handler) deleteAnalytics(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["streamId"]
	if _, err := h.fs.Collection("analytics").Doc(id).Delete(r.Context()); err != nil {
		jsonError(w, "failed to delete analytics", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"streamId": id, "deleted": true})
}

func (h *handler) topAnalytics(w http.ResponseWriter, r *http.Request) {
	docs, err := listDocs[AnalyticsDoc](r.Context(), h.fs.Collection("analytics").Limit(parseLimit(r.URL.Query().Get("limit"), 50)))
	if err != nil {
		jsonError(w, "failed to list analytics", http.StatusInternalServerError)
		return
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].PeakViewers == docs[j].PeakViewers {
			return docs[i].TotalJoins > docs[j].TotalJoins
		}
		return docs[i].PeakViewers > docs[j].PeakViewers
	})
	jsonOK(w, docs)
}

func (h *handler) listMatches(w http.ResponseWriter, r *http.Request) {
	matches, err := listDocs[Match](r.Context(), h.fs.Collection("matches").Limit(parseLimit(r.URL.Query().Get("limit"), 100)))
	if err != nil {
		jsonError(w, "failed to list matches", http.StatusInternalServerError)
		return
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ScheduledAt.After(matches[j].ScheduledAt) })
	jsonOK(w, matches)
}

func (h *handler) createMatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string `json:"title"`
		ScheduledAt string `json:"scheduledAt"`
		Status      string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Title) == "" || strings.TrimSpace(body.ScheduledAt) == "" {
		jsonError(w, "title and scheduledAt are required", http.StatusBadRequest)
		return
	}
	tm, err := time.Parse(time.RFC3339, body.ScheduledAt)
	if err != nil {
		jsonError(w, "scheduledAt must be RFC3339", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("match_%d", now.UnixMilli())
	m := map[string]any{"id": id, "title": body.Title, "scheduledAt": tm.UTC(), "status": strings.TrimSpace(body.Status), "createdAt": now, "updatedAt": now}
	if m["status"] == "" {
		m["status"] = "scheduled"
	}
	if _, err := h.fs.Collection("matches").Doc(id).Set(r.Context(), m); err != nil {
		jsonError(w, "failed to create match", http.StatusInternalServerError)
		return
	}
	jsonOK(w, m)
}

func (h *handler) getMatch(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	snap, err := h.fs.Collection("matches").Doc(id).Get(r.Context())
	if err != nil {
		jsonError(w, "match not found", http.StatusNotFound)
		return
	}
	var m Match
	if err := snap.DataTo(&m); err != nil {
		jsonError(w, "decode error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, m)
}

func (h *handler) updateMatch(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	updates := []firestore.Update{{Path: "updatedAt", Value: time.Now().UTC()}}
	if raw, ok := body["scheduledAt"].(string); ok && strings.TrimSpace(raw) != "" {
		tm, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			jsonError(w, "scheduledAt must be RFC3339", http.StatusBadRequest)
			return
		}
		body["scheduledAt"] = tm.UTC()
	}
	for k, v := range body {
		updates = append(updates, firestore.Update{Path: k, Value: v})
	}
	if _, err := h.fs.Collection("matches").Doc(id).Update(r.Context(), updates); err != nil {
		jsonError(w, "failed to update match", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"matchId": id, "updated": true})
}

func (h *handler) deleteMatch(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, err := h.fs.Collection("matches").Doc(id).Delete(r.Context()); err != nil {
		jsonError(w, "failed to delete match", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"matchId": id, "deleted": true})
}

func forwardJSON(method, url string, payload any) (int, map[string]any, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewBuffer(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	decoded := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&decoded)
	return res.StatusCode, decoded, nil
}

func (h *handler) sendNotification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventType string `json:"eventType"`
		StreamID  string `json:"streamId"`
		Title     string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.EventType) == "" {
		jsonError(w, "eventType is required", http.StatusBadRequest)
		return
	}
	status, resp, err := forwardJSON(http.MethodPost, "http://127.0.0.1:8083/api/v1/notifications/test", body)
	if err != nil {
		jsonError(w, "failed to call notification service", http.StatusBadGateway)
		return
	}
	if status >= 400 {
		msg := "notification service error"
		if v, ok := resp["message"].(string); ok && v != "" {
			msg = v
		}
		jsonError(w, msg, status)
		return
	}
	jsonOK(w, resp)
}

func (h *handler) systemHealth(w http.ResponseWriter, r *http.Request) {
	services := []map[string]string{
		{"name": "gateway", "url": "http://127.0.0.1:8080/health"},
		{"name": "user", "url": "http://127.0.0.1:8081/health"},
		{"name": "stream", "url": "http://127.0.0.1:8082/health"},
		{"name": "notification", "url": "http://127.0.0.1:8083/health"},
		{"name": "admin", "url": "http://127.0.0.1:8084/health"},
		{"name": "analytics", "url": "http://127.0.0.1:8085/health"},
	}
	out := make([]map[string]string, 0, len(services))
	cli := &http.Client{Timeout: 3 * time.Second}
	for _, s := range services {
		row := map[string]string{"name": s["name"], "url": s["url"], "status": "down", "details": ""}
		res, err := cli.Get(s["url"])
		if err != nil {
			row["details"] = err.Error()
			out = append(out, row)
			continue
		}
		_ = res.Body.Close()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			row["status"] = "up"
			row["details"] = "ok"
		} else {
			row["details"] = fmt.Sprintf("status %d", res.StatusCode)
		}
		out = append(out, row)
	}
	jsonOK(w, out)
}

func (h *handler) analyticsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	snaps := h.fs.Collection("analytics").Snapshots(ctx)
	defer snaps.Stop()

	for {
		snap, err := snaps.Next()
		if err != nil {
			return // client disconnected or Firestore error
		}
		docs := make([]AnalyticsDoc, 0)
		for {
			d, err := snap.Documents.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				break
			}
			var a AnalyticsDoc
			if e := d.DataTo(&a); e == nil {
				docs = append(docs, a)
			}
		}
		data, err := json.Marshal(docs)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
}

func adminPanelHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sports Stream Admin</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"></script>
<style>
:root{--bg:#f4efe6;--bg2:#eadfcf;--ink:#1f2937;--muted:#6b7280;--card:#fffaf2;--line:#e6d7c2;--accent:#bc4b36;--accent-2:#d98d43;--accent-dark:#7a2d25;--sidebar:#1f232d;--sidebar-2:#2d3240;--good:#0f9f6e;--bad:#d15241;--shadow:0 18px 50px rgba(55,38,24,.12)}
*{box-sizing:border-box}html,body{margin:0;min-height:100%}body{font-family:Georgia,"Iowan Old Style",serif;background:radial-gradient(circle at top left,#fbf7f1 0,#f2e7d8 45%,#e6d6c1 100%);color:var(--ink)}
button,input,select,textarea{font:inherit}
.app{display:grid;grid-template-columns:280px 1fr;min-height:100vh}
.sidebar{background:linear-gradient(180deg,var(--sidebar) 0,var(--sidebar-2) 100%);color:#f6ecdf;padding:28px 22px;border-right:1px solid rgba(255,255,255,.08);position:relative;overflow:hidden}
.sidebar:before,.sidebar:after{content:"";position:absolute;border-radius:999px;background:rgba(255,255,255,.05)}
.sidebar:before{width:220px;height:220px;top:-60px;right:-100px}.sidebar:after{width:140px;height:140px;bottom:40px;left:-60px}
.brand{position:relative;z-index:1;margin-bottom:28px}.brand-kicker{font:600 11px/1.2 ui-sans-serif,system-ui,sans-serif;letter-spacing:.28em;text-transform:uppercase;color:#d8bf9f}.brand h1{margin:10px 0 8px;font-size:30px;line-height:1}.brand p{margin:0;color:#c7b79f;font:500 14px/1.5 ui-sans-serif,system-ui,sans-serif}
.menu{position:relative;z-index:1;display:grid;gap:10px}.menu button{display:flex;align-items:center;justify-content:space-between;width:100%;padding:14px 16px;border-radius:18px;border:1px solid rgba(255,255,255,.08);background:rgba(255,255,255,.05);color:#f7efe2;cursor:pointer;transition:transform .15s ease,background .15s ease,border-color .15s ease}.menu button span{font:600 13px/1.2 ui-sans-serif,system-ui,sans-serif;letter-spacing:.08em;text-transform:uppercase}.menu button small{color:#ccb99f;font:500 11px/1.2 ui-sans-serif,system-ui,sans-serif}.menu button:hover{transform:translateX(3px);background:rgba(255,255,255,.08)}.menu button.active{background:linear-gradient(135deg,var(--accent) 0,var(--accent-2) 100%);border-color:transparent;box-shadow:0 18px 30px rgba(188,75,54,.24)}
.sidebar-foot{position:relative;z-index:1;margin-top:24px;padding:16px;border:1px solid rgba(255,255,255,.08);border-radius:18px;background:rgba(255,255,255,.05)}
.sidebar-foot strong{display:block;margin-bottom:6px;font:700 12px/1.2 ui-sans-serif,system-ui,sans-serif;letter-spacing:.18em;text-transform:uppercase;color:#f3e5d1}.sidebar-foot p{margin:0;color:#ccb99f;font:500 13px/1.5 ui-sans-serif,system-ui,sans-serif}
.main{padding:28px}.shell{max-width:1400px;margin:0 auto}.topbar{display:flex;justify-content:space-between;gap:16px;align-items:flex-start;margin-bottom:18px}.title-wrap h2{margin:0;font-size:38px;line-height:1.05}.title-wrap p{margin:8px 0 0;color:#75624e;font:500 15px/1.5 ui-sans-serif,system-ui,sans-serif}.top-actions{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.surface{background:rgba(255,250,243,.82);backdrop-filter:blur(12px);border:1px solid rgba(120,90,61,.14);border-radius:28px;box-shadow:var(--shadow);padding:20px}
.toolbar{display:flex;gap:10px;flex-wrap:wrap;margin-bottom:18px}.toolbar input,.toolbar select,.toolbar textarea,.toolbar button,.input{padding:12px 14px;border:1px solid var(--line);border-radius:14px;background:#fffdf8;color:var(--ink);min-height:46px}.toolbar textarea{min-height:110px;resize:vertical}
.btn{border:none;background:linear-gradient(135deg,var(--accent) 0,var(--accent-2) 100%);color:#fff;cursor:pointer;box-shadow:0 12px 22px rgba(188,75,54,.22)}.btn.subtle{background:#f5ebde;color:#7d6247;box-shadow:none;border:1px solid var(--line)}.btn.ghost{background:rgba(255,255,255,.74);color:var(--ink);box-shadow:none;border:1px solid var(--line)}.btn.warn{background:linear-gradient(135deg,#b44232 0,#d86f43 100%)}
.stack{display:grid;gap:18px}.hero{display:grid;grid-template-columns:1.5fr .9fr;gap:18px;margin-bottom:18px}.hero-card{padding:24px;border-radius:24px;background:linear-gradient(135deg,#fff9f0 0,#f6ead9 100%);border:1px solid var(--line)}.hero-card h3{margin:0 0 10px;font-size:28px}.hero-card p{margin:0;color:#6f5c47;font:500 15px/1.6 ui-sans-serif,system-ui,sans-serif}.hero-side{display:grid;gap:14px}
.badge{display:inline-flex;align-items:center;gap:8px;padding:8px 12px;border-radius:999px;background:#f3e6d7;color:#7b5f44;border:1px solid var(--line);font:700 11px/1 ui-sans-serif,system-ui,sans-serif;letter-spacing:.12em;text-transform:uppercase}.badge.good{background:#e2f5ed;color:#0e7c57;border-color:#b6e6d2}.badge.bad{background:#fde7e3;color:#b14234;border-color:#f6c0b8}.badge.live{background:#ffe7dd;color:#b54835;border-color:#f1beaa}
.dot{width:8px;height:8px;border-radius:50%;background:#c7b59f}.dot.good{background:#1dbf87;box-shadow:0 0 10px rgba(29,191,135,.45)}.dot.bad{background:#e05e48;box-shadow:0 0 10px rgba(224,94,72,.4)}
.grid{display:grid;gap:16px}.stats{grid-template-columns:repeat(auto-fit,minmax(190px,1fr))}.stat{padding:18px;border-radius:22px;background:linear-gradient(180deg,#fffdf9 0,#fff2e4 100%);border:1px solid var(--line)}.stat .label{color:#8a6f53;font:700 11px/1 ui-sans-serif,system-ui,sans-serif;letter-spacing:.14em;text-transform:uppercase}.stat .value{margin-top:10px;font-size:34px;line-height:1.05}.stat .meta{margin-top:8px;color:#7f6750;font:500 13px/1.5 ui-sans-serif,system-ui,sans-serif}
.panel{padding:18px;border-radius:22px;background:#fffdf8;border:1px solid var(--line)}.panel h3{margin:0 0 6px;font-size:22px}.panel p{margin:0;color:#7a6651;font:500 14px/1.5 ui-sans-serif,system-ui,sans-serif}
.split{display:grid;grid-template-columns:1.15fr .85fr;gap:18px}.form-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px}.form-grid.wide{grid-template-columns:repeat(auto-fit,minmax(220px,1fr))}
.tbl-wrap{overflow:auto;border-radius:20px;border:1px solid var(--line);background:#fffdf9}.tbl{width:100%;border-collapse:collapse;min-width:760px}.tbl td,.tbl th{padding:14px 16px;border-bottom:1px solid #efe3d1;vertical-align:top}.tbl th{background:#f8efe2;color:#6f563a;font:700 11px/1.2 ui-sans-serif,system-ui,sans-serif;letter-spacing:.12em;text-transform:uppercase;text-align:left}.tbl tr:hover td{background:#fff8ef}.tbl td strong{display:block;margin-bottom:4px}.tbl td small{display:block;color:#8a7054;font:500 12px/1.4 ui-sans-serif,system-ui,sans-serif}
.row-actions{display:flex;gap:8px;flex-wrap:wrap}.row-actions button{padding:10px 12px;border-radius:12px;border:1px solid var(--line);background:#fff9f1;cursor:pointer}.row-actions .primary{background:linear-gradient(135deg,var(--accent) 0,var(--accent-2) 100%);border:none;color:#fff}
.empty{padding:28px;border-radius:20px;border:1px dashed #d8c3a7;background:#fff8ef;color:#7d6449;text-align:center;font:500 14px/1.6 ui-sans-serif,system-ui,sans-serif}
.code{padding:16px;border-radius:16px;background:#211f1b;color:#f6ead6;overflow:auto;font:13px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;border:1px solid rgba(255,255,255,.08)}
.leaderboard{display:grid;gap:12px}.leader{padding:14px 16px;border-radius:18px;background:linear-gradient(135deg,#fff5ea 0,#fffdfb 100%);border:1px solid var(--line)}.leader-head{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-bottom:8px}.leader-title{font-weight:700}.leader-meta{color:#8a7054;font:500 12px/1.4 ui-sans-serif,system-ui,sans-serif}.meter{height:10px;border-radius:999px;background:#f1e3d2;overflow:hidden;margin-top:10px}.meter span{display:block;height:100%;background:linear-gradient(90deg,var(--accent) 0,var(--accent-2) 100%)}
.chart-row{display:grid;grid-template-columns:1fr 1fr;gap:18px}.chart-wrap{position:relative;height:260px}
.login-shell{min-height:100vh;display:grid;place-items:center;padding:28px;background:radial-gradient(circle at top left,#fcf8f2 0,#efe1ce 44%,#dec7a9 100%)}
.sse-badge{display:inline-flex;align-items:center;gap:8px;padding:8px 12px;border-radius:999px;border:1px solid var(--line);background:#fff8ee;color:#765f47;font:700 11px/1 ui-sans-serif,system-ui,sans-serif;letter-spacing:.12em;text-transform:uppercase}
.sse-dot{width:8px;height:8px;border-radius:50%;background:#cbbda9}.sse-dot.on{background:#19b97f;box-shadow:0 0 10px rgba(25,185,127,.45)}.sse-dot.err{background:#e05e48;box-shadow:0 0 10px rgba(224,94,72,.45)}
.toast-root{position:fixed;top:18px;right:18px;display:grid;gap:10px;z-index:9999;max-width:min(360px,calc(100vw - 36px))}
.toast{padding:14px 16px;border-radius:16px;border:1px solid var(--line);box-shadow:0 16px 32px rgba(45,29,17,.16);background:rgba(255,251,245,.96);color:#2c241b;font:600 13px/1.5 ui-sans-serif,system-ui,sans-serif;transform:translateY(-4px);opacity:0;transition:opacity .18s ease,transform .18s ease}.toast.show{opacity:1;transform:translateY(0)}.toast.success{border-color:#bce6d4;background:#f0fbf6}.toast.error{border-color:#f0c1b9;background:#fff2ef}
.muted{color:#88705a}.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}.pill{display:inline-flex;align-items:center;padding:7px 11px;border-radius:999px;background:#f1e6d7;color:#7c6146;border:1px solid var(--line);font:700 11px/1 ui-sans-serif,system-ui,sans-serif;letter-spacing:.08em;text-transform:uppercase}.pill.live{background:#fee5da;color:#b64935;border-color:#efc1ae}
@media (max-width:1180px){.app{grid-template-columns:1fr}.sidebar{padding-bottom:20px}.main{padding:20px}.hero,.split,.chart-row{grid-template-columns:1fr}}
@media (max-width:720px){.topbar{flex-direction:column}.title-wrap h2{font-size:30px}.toolbar,.form-grid{grid-template-columns:1fr}.tbl{min-width:620px}}
</style>
</head>
<body>
<div class="app">
	<aside class="sidebar">
		<div class="brand">
			<div class="brand-kicker">Control Center</div>
			<h1>Sports Stream</h1>
			<p>Operations, live telemetry, publishing, and health checks in one place.</p>
		</div>
		<nav id="menu" class="menu"></nav>
		<div class="sidebar-foot">
			<strong>Operations Notes</strong>
			<p>All actions call the existing admin APIs directly. Analytics updates stream live over SSE without polling.</p>
		</div>
	</aside>
	<main class="main">
		<div class="shell">
			<div class="topbar">
				<div class="title-wrap">
					<h2 id="title">Dashboard</h2>
					<p id="subtitle">Overview of users, streams, and system activity.</p>
				</div>
				<div class="top-actions">
					<div class="badge good"><span class="dot good"></span>Admin Session</div>
					<button class="btn subtle" onclick="logout()">Logout</button>
				</div>
			</div>
			<section class="surface">
				<div id="toolbar" class="toolbar"></div>
				<div id="body"></div>
			</section>
		</div>
	</main>
</div>
<div id="toast-root" class="toast-root"></div>
<script>
var pages=[
	{id:'dashboard',label:'Dashboard',meta:'Overview'},
	{id:'users',label:'Users',meta:'Identity'},
	{id:'streams',label:'Streams',meta:'Broadcasts'},
	{id:'analytics',label:'Analytics',meta:'Live graph'},
	{id:'matches',label:'Matches',meta:'Schedule'},
	{id:'notifications',label:'Alerts',meta:'Push'},
	{id:'health',label:'Health',meta:'Services'}
];
var curr='dashboard';
var analyticsSSE=null;var analyticsBarChart=null;var analyticsLineChart=null;var viewerTrend=[];var cachedStreams={};var MAX_TREND=20;
function menu(){var m=document.getElementById('menu');var h='';for(var i=0;i<pages.length;i++){var p=pages[i];h+='<button class="'+(p.id===curr?'active':'')+'" onclick="go(\''+p.id+'\')"><span>'+p.label+'</span><small>'+p.meta+'</small></button>';}m.innerHTML=h;}
async function api(path,opts){opts=opts||{};var r=await fetch(path,{method:opts.method||'GET',headers:{'Content-Type':'application/json'},body:opts.body||undefined});var b=await r.json();if(!r.ok||!b.success)throw new Error(b.message||'Request failed');return b.data;}
async function safeApi(path,fallback){try{return {ok:true,data:await api(path)};}catch(e){return {ok:false,data:fallback,error:(e&&e.message)?e.message:'Request failed'};}}
function set(title,subtitle,toolbar,body){document.getElementById('title').textContent=title;document.getElementById('subtitle').textContent=subtitle||'';document.getElementById('toolbar').innerHTML=toolbar||'';document.getElementById('body').innerHTML=body||'';}
function esc(v){return String(v==null?'':v).replace(/[&<>"']/g,function(ch){return({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[ch];});}
function n(v){var num=Number(v||0);return isNaN(num)?'0':num.toLocaleString();}
function fmtDate(v){if(!v)return 'No timestamp';var d=new Date(v);return isNaN(d.getTime())?String(v):d.toLocaleString();}
function gid(id){var e=document.getElementById(id);return e?e.value.trim():'';}
function pageIntro(kicker,title,desc,extra){return '<div class="hero"><div class="hero-card"><div class="badge">'+esc(kicker)+'</div><h3>'+esc(title)+'</h3><p>'+esc(desc)+'</p></div><div class="hero-side">'+(extra||'')+'</div></div>';}
function metric(label,value,meta){return '<div class="stat"><div class="label">'+esc(label)+'</div><div class="value">'+esc(value)+'</div><div class="meta">'+esc(meta||'')+'</div></div>';}
function panel(title,desc,content){return '<section class="panel"><h3>'+esc(title)+'</h3><p>'+esc(desc||'')+'</p><div style="margin-top:14px">'+(content||'')+'</div></section>';}
function emptyState(msg){return '<div class="empty">'+esc(msg)+'</div>';}
function badge(text,kind){return '<span class="badge'+(kind?' '+kind:'')+'">'+esc(text)+'</span>';}
function pill(text,live){return '<span class="pill'+(live?' live':'')+'">'+esc(text)+'</span>';}
function table(headers,rows){var h='<div class="tbl-wrap"><table class="tbl"><tr>';for(var i=0;i<headers.length;i++)h+='<th>'+headers[i]+'</th>';h+='</tr>'+rows+'</table></div>';return h;}
function rowActions(btns){return '<div class="row-actions">'+btns.join('')+'</div>';}
function showToast(kind,msg){var root=document.getElementById('toast-root');if(!root)return;var el=document.createElement('div');el.className='toast '+kind;el.textContent=msg||'';root.appendChild(el);requestAnimationFrame(function(){el.classList.add('show');});setTimeout(function(){el.classList.remove('show');setTimeout(function(){if(el.parentNode)el.parentNode.removeChild(el);},220);},3200);}
function notifyError(e){showToast('error',(e&&e.message)?e.message:'Operation failed');}
function notifySuccess(msg){showToast('success',msg||'Saved');}
function stopAnalyticsSSE(){if(analyticsSSE){analyticsSSE.close();analyticsSSE=null;}if(analyticsBarChart){analyticsBarChart.destroy();analyticsBarChart=null;}if(analyticsLineChart){analyticsLineChart.destroy();analyticsLineChart=null;}}
function setSSEStatus(s,l){var d=document.getElementById('sse-dot');var lb=document.getElementById('sse-lbl');if(d)d.className='sse-dot '+(s==='on'?'on':s==='err'?'err':'');if(lb)lb.textContent=l;}
function updateAnalyticsStatsEl(docs){var el=document.getElementById('a-stats');if(!el)return;var tc=docs.reduce(function(sum,d){return sum+Number(d.currentViewers||0);},0),tp=docs.reduce(function(sum,d){return sum+Number(d.peakViewers||0);},0),tj=docs.reduce(function(sum,d){return sum+Number(d.totalJoins||0);},0),lv=docs.filter(function(d){return Number(d.currentViewers||0)>0;}).length;el.innerHTML='<div class="grid stats">'+metric('Tracked Streams',n(docs.length),n(lv)+' currently active')+metric('Current Viewers',n(tc),'Concurrent viewers across all tracked streams')+metric('Peak Viewers',n(tp),'Sum of per-stream peak values')+metric('Total Joins',n(tj),'All accumulated join events')+'</div>';}
function renderAnalyticsTable(docs){var container=document.getElementById('a-table');if(!container)return;var rows='';for(var i=0;i<docs.length;i++){var a=docs[i];var s=cachedStreams[a.streamId]||{};var engagement=Number(a.peakViewers||0)>0?Math.round((Number(a.currentViewers||0)/Math.max(Number(a.peakViewers||0),1))*100):0;rows+='<tr><td><strong>'+esc(s.title||a.streamId)+'</strong><small class="mono">'+esc(a.streamId)+'</small></td><td>'+pill(s.status||'unknown',(s.status||'')==='live')+'<small>'+(s.broadcasterUid?esc(s.broadcasterUid):'No broadcaster')+'</small></td><td><strong>'+n(a.currentViewers)+'</strong><small>Live now</small></td><td><strong>'+n(a.peakViewers)+'</strong><small>Best observed</small></td><td><strong>'+n(a.totalJoins)+'</strong><small>Cumulative joins</small></td><td><strong>'+n(engagement)+'%</strong><small>Current / peak</small></td><td><strong>'+fmtDate(a.updatedAt)+'</strong><small>Last update</small></td></tr>';}
container.innerHTML=docs.length?table(['Stream','Status','Current','Peak','Joins','Engagement','Updated'],rows):emptyState('No analytics data yet. Viewer activity will appear here automatically.');}
function renderLeaderboard(top){var html='';if(!top.length)return emptyState('No ranked streams available yet.');var maxPeak=Math.max.apply(null,top.map(function(item){return Number(item.peakViewers||0)||1;}));for(var i=0;i<top.length;i++){var row=top[i];var s=cachedStreams[row.streamId]||{};var width=Math.max(10,Math.round((Number(row.peakViewers||0)/Math.max(maxPeak,1))*100));html+='<div class="leader"><div class="leader-head"><div><div class="leader-title">'+esc(s.title||row.streamId)+'</div><div class="leader-meta mono">'+esc(row.streamId)+'</div></div>'+pill(s.status||'tracked',(s.status||'')==='live')+'</div><div class="leader-meta">Peak '+n(row.peakViewers)+' • Current '+n(row.currentViewers)+' • Joins '+n(row.totalJoins)+'</div><div class="meter"><span style="width:'+width+'%"></span></div></div>';}return '<div class="leaderboard">'+html+'</div>';}
function initCharts(analytics){var bar=document.getElementById('chart-bar');if(bar){var labels=analytics.map(function(d){return(cachedStreams[d.streamId]&&cachedStreams[d.streamId].title)||d.streamId;});analyticsBarChart=new Chart(bar,{type:'bar',data:{labels:labels,datasets:[{label:'Current',data:analytics.map(function(d){return Number(d.currentViewers||0);}),backgroundColor:'rgba(188,75,54,.78)',borderColor:'#bc4b36',borderWidth:1,borderRadius:10},{label:'Peak',data:analytics.map(function(d){return Number(d.peakViewers||0);}),backgroundColor:'rgba(217,141,67,.38)',borderColor:'#d98d43',borderWidth:1,borderRadius:10}]},options:{responsive:true,maintainAspectRatio:false,animation:false,plugins:{legend:{labels:{usePointStyle:true}}},scales:{x:{ticks:{color:'#7b644a'}},y:{beginAtZero:true,ticks:{color:'#7b644a'}}}}});}
var line=document.getElementById('chart-line');if(line){var total=analytics.reduce(function(sum,d){return sum+Number(d.currentViewers||0);},0);var now=new Date();var stamp=now.getHours()+':'+String(now.getMinutes()).padStart(2,'0')+':'+String(now.getSeconds()).padStart(2,'0');viewerTrend=[{t:stamp,v:total}];analyticsLineChart=new Chart(line,{type:'line',data:{labels:[stamp],datasets:[{label:'Total Live Viewers',data:[total],borderColor:'#bc4b36',backgroundColor:'rgba(188,75,54,.12)',fill:true,tension:.34,pointRadius:3,pointBackgroundColor:'#bc4b36'}]},options:{responsive:true,maintainAspectRatio:false,animation:false,plugins:{legend:{labels:{usePointStyle:true}}},scales:{x:{ticks:{color:'#7b644a'}},y:{beginAtZero:true,ticks:{color:'#7b644a'}}}}});}}
function updateChartsFromSSE(docs){docs.sort(function(a,b){return Number(b.currentViewers||0)-Number(a.currentViewers||0)||Number(b.peakViewers||0)-Number(a.peakViewers||0);});setSSEStatus('on','Live');if(analyticsBarChart){analyticsBarChart.data.labels=docs.map(function(d){return(cachedStreams[d.streamId]&&cachedStreams[d.streamId].title)||d.streamId;});analyticsBarChart.data.datasets[0].data=docs.map(function(d){return Number(d.currentViewers||0);});analyticsBarChart.data.datasets[1].data=docs.map(function(d){return Number(d.peakViewers||0);});analyticsBarChart.update('none');}var total=docs.reduce(function(sum,d){return sum+Number(d.currentViewers||0);},0);var now=new Date();var stamp=now.getHours()+':'+String(now.getMinutes()).padStart(2,'0')+':'+String(now.getSeconds()).padStart(2,'0');viewerTrend.push({t:stamp,v:total});if(viewerTrend.length>MAX_TREND)viewerTrend.shift();if(analyticsLineChart){analyticsLineChart.data.labels=viewerTrend.map(function(p){return p.t;});analyticsLineChart.data.datasets[0].data=viewerTrend.map(function(p){return p.v;});analyticsLineChart.update('none');}updateAnalyticsStatsEl(docs);renderAnalyticsTable(docs);}
function startAnalyticsSSE(){analyticsSSE=new EventSource('/api/v1/admin/analytics/events');analyticsSSE.onopen=function(){setSSEStatus('on','Live');};analyticsSSE.onmessage=function(e){try{updateChartsFromSSE(JSON.parse(e.data));}catch(err){console.error(err);}};analyticsSSE.onerror=function(){setSSEStatus('err','Reconnecting');};}
async function renderDashboard(){var d=await api('/api/v1/admin/dashboard');var body=pageIntro('Operations','Broadcast control and usage overview','Use this workspace to manage accounts, streams, notifications, schedules, and live analytics.',panel('Quick actions','Jump straight into the most common workflows.','<div class="row-actions"><button class="btn" onclick="go(\'streams\')">Manage live streams</button><button class="btn subtle" onclick="go(\'analytics\')">Open analytics</button><button class="btn subtle" onclick="go(\'health\')">Check health</button></div>'))+'<div class="grid stats">'+metric('Users',n(d.usersTotal),n(d.viewers)+' viewers in the system')+metric('Admins',n(d.adminsTotal),'Privileged panel access')+metric('Broadcasters',n(d.broadcasters),'Accounts allowed to publish')+metric('Live Streams',n(d.liveStreams),'Streams currently marked live')+metric('Current Viewers',n(d.currentViewers),'Real-time concurrent viewers')+metric('Total Joins',n(d.totalJoinsAll),'All analytics joins recorded')+'</div>';
set('Dashboard','Executive summary for operations, growth, and live service load.','<button class="btn" onclick="renderDashboard()">Refresh overview</button>',body);}
async function renderUsers(){var u=await api('/api/v1/admin/users?limit=200');var rows='';for(var i=0;i<u.length;i++){var x=u[i];rows+='<tr><td><strong>'+esc(x.uid)+'</strong><small>'+fmtDate(x.updatedAt)+'</small></td><td><strong>'+esc(x.displayName||'Unnamed user')+'</strong><small>'+esc(x.email||'No email')+'</small></td><td>'+pill(x.role||'viewer',x.role==='admin')+'</td><td><input class="input" id="role_'+x.uid+'" value="'+esc(x.role||'viewer')+'"></td><td>'+rowActions(['<button class="primary" onclick="saveRole(\''+x.uid+'\')">Save role</button>','<button onclick="delUser(\''+x.uid+'\')">Delete</button>'])+'</td></tr>';}
var body=pageIntro('Identity','User management','Create users, update roles, and inspect the latest account activity.',panel('Create user','Provision a new identity record in Firestore.','<div class="form-grid wide"><input class="input" id="u_uid" placeholder="UID"><input class="input" id="u_email" placeholder="Email"><input class="input" id="u_name" placeholder="Display name"><input class="input" id="u_role" placeholder="Role"><button class="btn" onclick="createUser()">Create user</button></div>'))+panel('Existing users','Role edits and deletions apply immediately to the stored user profile.',u.length?table(['UID','Profile','Role','Edit role','Actions'],rows):emptyState('No users found.'));
set('Users','Identity operations and role administration.','<button class="btn subtle" onclick="renderUsers()">Refresh users</button>',body);}
async function createUser(){await api('/api/v1/admin/users',{method:'POST',body:JSON.stringify({uid:gid('u_uid'),email:gid('u_email'),displayName:gid('u_name'),role:gid('u_role')||'viewer'})});notifySuccess('User created');renderUsers();}
async function saveRole(uid){await api('/api/v1/admin/users/'+uid+'/role',{method:'PATCH',body:JSON.stringify({role:gid('role_'+uid)})});notifySuccess('Role updated');renderUsers();}
async function delUser(uid){if(!confirm('Delete user '+uid+'?'))return;await api('/api/v1/admin/users/'+uid,{method:'DELETE'});notifySuccess('User deleted');renderUsers();}
async function renderStreams(){var s=await api('/api/v1/admin/streams?limit=200');var rows='';for(var i=0;i<s.length;i++){var x=s[i];rows+='<tr><td><strong>'+esc(x.id)+'</strong><small>'+fmtDate(x.updatedAt)+'</small></td><td><input class="input" id="st_t_'+x.id+'" value="'+esc(x.title||'')+'"></td><td><input class="input" id="st_s_'+x.id+'" value="'+esc(x.status||'')+'"></td><td><input class="input" id="st_v_'+x.id+'" value="'+esc(x.viewerCount||0)+'"></td><td><small class="mono">'+esc(x.broadcasterUid||'No broadcaster')+'</small></td><td>'+rowActions(['<button class="primary" onclick="saveStream(\''+x.id+'\')">Save</button>','<button onclick="endStream(\''+x.id+'\')">End</button>','<button onclick="resetViewers(\''+x.id+'\')">Reset</button>','<button onclick="delStream(\''+x.id+'\')">Delete</button>'])+'</td></tr>';}
var body=pageIntro('Broadcasts','Stream control','Update titles, change status, end sessions, or reset viewer counters from one view.',panel('Create stream','Create a stream record used by the broadcast services.','<div class="form-grid wide"><input class="input" id="s_title" placeholder="Title"><input class="input" id="s_status" placeholder="Status"><input class="input" id="s_uid" placeholder="Broadcaster UID"><button class="btn" onclick="createStream()">Create stream</button></div>'))+panel('Stream inventory','Live operational stream records sorted by recent updates.',s.length?table(['Stream ID','Title','Status','Viewers','Broadcaster','Actions'],rows):emptyState('No streams found.'));
set('Streams','Broadcast configuration and live-session controls.','<button class="btn subtle" onclick="renderStreams()">Refresh streams</button>',body);}
async function createStream(){await api('/api/v1/admin/streams',{method:'POST',body:JSON.stringify({title:gid('s_title'),status:gid('s_status')||'live',broadcasterUid:gid('s_uid')})});notifySuccess('Stream created');renderStreams();}
async function saveStream(id){await api('/api/v1/admin/streams/'+id,{method:'PATCH',body:JSON.stringify({title:gid('st_t_'+id),status:gid('st_s_'+id),viewerCount:Number(gid('st_v_'+id)||0)})});notifySuccess('Stream updated');renderStreams();}
async function endStream(id){await api('/api/v1/admin/streams/'+id+'/end',{method:'POST'});notifySuccess('Stream ended');renderStreams();}
async function resetViewers(id){await api('/api/v1/admin/streams/'+id+'/reset-viewers',{method:'POST'});notifySuccess('Viewer count reset');renderStreams();}
async function delStream(id){if(!confirm('Delete stream '+id+'?'))return;await api('/api/v1/admin/streams/'+id,{method:'DELETE'});notifySuccess('Stream deleted');renderStreams();}
async function renderAnalytics(silent){stopAnalyticsSSE();viewerTrend=[];set('Analytics','Live telemetry dashboard with streaming updates and ranked stream visibility.','<span class="sse-badge"><span id="sse-dot" class="sse-dot"></span><span id="sse-lbl">Connecting</span></span><button class="btn subtle" onclick="renderAnalytics()">Reset view</button>','<div class="stack">'+pageIntro('Telemetry','Live audience analytics','Viewer counts and join totals update from Firestore snapshots without interval polling.',panel('Feed status','The charts below remain connected as long as this page is active.','<div id="a-stats">'+emptyState('Loading analytics summary...')+'</div>'))+'<div class="chart-row"><section class="panel"><h3>Viewer trend</h3><p>Concurrent viewers across all tracked streams over recent updates.</p><div class="chart-wrap"><canvas id="chart-line"></canvas></div></section><section class="panel"><h3>Current vs peak</h3><p>Per-stream comparison of current load against observed peak.</p><div class="chart-wrap"><canvas id="chart-bar"></canvas></div></section></div><div class="split"><section class="panel"><h3>Tracked streams</h3><p>Detailed operational table for all analytics documents.</p><div id="a-table" style="margin-top:14px"></div></section><section class="panel"><h3>Top performers</h3><p>Ranked by peak viewers with current stream context.</p><div id="a-top" style="margin-top:14px">'+emptyState('Loading ranked streams...')+'</div></section></div></div>');
var res=await Promise.all([safeApi('/api/v1/admin/analytics?limit=200',[]),safeApi('/api/v1/admin/streams?limit=200',[]),safeApi('/api/v1/admin/analytics/top?limit=5',[])]);var analytics=Array.isArray(res[0].data)?res[0].data:[];var streams=Array.isArray(res[1].data)?res[1].data:[];var top=Array.isArray(res[2].data)?res[2].data:[];cachedStreams={};for(var i=0;i<streams.length;i++){cachedStreams[streams[i].id]=streams[i];}analytics.sort(function(a,b){return Number(b.currentViewers||0)-Number(a.currentViewers||0)||Number(b.peakViewers||0)-Number(a.peakViewers||0);});updateAnalyticsStatsEl(analytics);renderAnalyticsTable(analytics);document.getElementById('a-top').innerHTML=renderLeaderboard(top);initCharts(analytics);startAnalyticsSSE();if(!silent)window.scrollTo({top:0,behavior:'smooth'});}
async function renderMatches(){var m=await api('/api/v1/admin/matches?limit=200');var rows='';for(var i=0;i<m.length;i++){var x=m[i];rows+='<tr><td><strong>'+esc(x.id)+'</strong><small>'+fmtDate(x.updatedAt)+'</small></td><td><input class="input" id="mt_'+x.id+'" value="'+esc(x.title||'')+'"></td><td><input class="input" id="ma_'+x.id+'" value="'+esc(x.scheduledAt||'')+'"></td><td><input class="input" id="ms_'+x.id+'" value="'+esc(x.status||'scheduled')+'"></td><td>'+rowActions(['<button class="primary" onclick="saveMatch(\''+x.id+'\')">Save</button>','<button onclick="delMatch(\''+x.id+'\')">Delete</button>'])+'</td></tr>';}
var body=pageIntro('Scheduling','Match operations','Manage the match slate that supports the streaming schedule and admin workflows.',panel('Create match','Submit a new match with an RFC3339 scheduled timestamp.','<div class="form-grid wide"><input class="input" id="m_t" placeholder="Title"><input class="input" id="m_at" placeholder="ScheduledAt RFC3339"><input class="input" id="m_s" placeholder="Status"><button class="btn" onclick="createMatch()">Create match</button></div>'))+panel('Match list','Edit titles, schedule, or status inline.',m.length?table(['Match ID','Title','ScheduledAt','Status','Actions'],rows):emptyState('No matches found.'));
set('Matches','Scheduling and editorial metadata.','<button class="btn subtle" onclick="renderMatches()">Refresh matches</button>',body);}
async function createMatch(){await api('/api/v1/admin/matches',{method:'POST',body:JSON.stringify({title:gid('m_t'),scheduledAt:gid('m_at'),status:gid('m_s')||'scheduled'})});notifySuccess('Match created');renderMatches();}
async function saveMatch(id){await api('/api/v1/admin/matches/'+id,{method:'PATCH',body:JSON.stringify({title:gid('mt_'+id),scheduledAt:gid('ma_'+id),status:gid('ms_'+id)})});notifySuccess('Match updated');renderMatches();}
async function delMatch(id){if(!confirm('Delete match '+id+'?'))return;await api('/api/v1/admin/matches/'+id,{method:'DELETE'});notifySuccess('Match deleted');renderMatches();}
async function renderNotifications(){var body=pageIntro('Messaging','Manual event push','Trigger a notification payload that downstream services can consume.',panel('Send notification','Fill the event payload and dispatch it through the admin service.','<div class="stack"><div class="form-grid wide"><input class="input" id="n_e" placeholder="Event type"><input class="input" id="n_id" placeholder="Stream ID"><input class="input" id="n_t" placeholder="Title"></div><div class="row-actions"><button class="btn" onclick="sendNotification()">Send notification</button></div></div>'))+panel('Response payload','Server response is shown here after a successful request.','<pre id="n_out" class="code">No notification sent yet.</pre>');set('Notifications','Operational push tools for event-driven messaging.','',body);}
async function sendNotification(){var d=await api('/api/v1/admin/notifications/send',{method:'POST',body:JSON.stringify({eventType:gid('n_e'),streamId:gid('n_id'),title:gid('n_t')})});document.getElementById('n_out').textContent=JSON.stringify(d,null,2);notifySuccess('Notification sent');} 
async function renderHealth(){var h=await api('/api/v1/admin/system/health');var rows='';for(var i=0;i<h.length;i++){var item=h[i];var good=(item.status||'').toLowerCase()==='ok';rows+='<tr><td><strong>'+esc(item.name)+'</strong><small>'+esc(item.url||'')+'</small></td><td>'+badge(item.status||'unknown',good?'good':'bad')+'</td><td><small>'+esc(item.details||'No extra details')+'</small></td></tr>';}
var body=pageIntro('Reliability','Service health','Quick operational checks for dependent services behind the admin surface.',panel('Service status','This view pulls the aggregated health endpoint used by the admin service.',h.length?table(['Service','Status','Details'],rows):emptyState('No health data returned.')));set('Health','Runtime checks for dependent services.','<button class="btn subtle" onclick="renderHealth()">Refresh status</button>',body);}
function go(p){stopAnalyticsSSE();curr=p;menu();var map={dashboard:renderDashboard,users:renderUsers,streams:renderStreams,analytics:renderAnalytics,matches:renderMatches,notifications:renderNotifications,health:renderHealth};map[p]().catch(notifyError);}
async function logout(){await fetch('/admin/logout',{method:'POST'});window.location.href='/admin/login';}
menu();go('dashboard');
</script>
</body>
</html>`
}

func loginHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sports Stream Admin Login</title>
<style>
body{margin:0;min-height:100vh;font-family:Georgia,"Iowan Old Style",serif;background:radial-gradient(circle at top left,#fcf8f2 0,#efdfc9 46%,#dcc3a1 100%);color:#231c16}
.login-shell{min-height:100vh;display:grid;place-items:center;padding:24px}
.login-card{width:min(960px,100%);display:grid;grid-template-columns:1.1fr .9fr;border-radius:30px;overflow:hidden;background:rgba(255,250,242,.88);border:1px solid rgba(120,90,61,.14);box-shadow:0 28px 60px rgba(60,41,24,.16);backdrop-filter:blur(12px)}
.login-aside{padding:34px;background:linear-gradient(160deg,#222733 0,#363c4d 100%);color:#f8efe1;position:relative}.login-aside:before{content:"";position:absolute;right:-70px;top:-50px;width:220px;height:220px;border-radius:50%;background:rgba(255,255,255,.05)}
.login-aside .kicker{font:700 11px/1.1 ui-sans-serif,system-ui,sans-serif;letter-spacing:.28em;text-transform:uppercase;color:#d9c3a6}.login-aside h1{margin:14px 0 10px;font-size:40px;line-height:1}.login-aside p{margin:0;color:#d1c0a8;font:500 15px/1.7 ui-sans-serif,system-ui,sans-serif}
.login-points{margin-top:26px;display:grid;gap:12px}.login-point{padding:14px 16px;border-radius:18px;background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.08)}.login-point strong{display:block;margin-bottom:6px;font:700 12px/1.2 ui-sans-serif,system-ui,sans-serif;letter-spacing:.16em;text-transform:uppercase}.login-point span{color:#d1c0a8;font:500 13px/1.5 ui-sans-serif,system-ui,sans-serif}
.login-main{padding:34px}.login-main h2{margin:0 0 10px;font-size:32px}.login-main p{margin:0 0 20px;color:#76624d;font:500 15px/1.6 ui-sans-serif,system-ui,sans-serif}.field{display:grid;gap:8px;margin-bottom:14px}.field label{font:700 12px/1.2 ui-sans-serif,system-ui,sans-serif;letter-spacing:.16em;text-transform:uppercase;color:#7f6548}.field input{width:100%;padding:14px 16px;border-radius:16px;border:1px solid #ddcdb7;background:#fffdf9;color:#231c16}.actions{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-top:10px}.actions button{padding:14px 18px;border:none;border-radius:16px;background:linear-gradient(135deg,#bc4b36 0,#d98d43 100%);color:#fff;cursor:pointer;box-shadow:0 14px 24px rgba(188,75,54,.22)}.hint{color:#8a7156;font:500 13px/1.5 ui-sans-serif,system-ui,sans-serif}.err{min-height:22px;margin-top:12px;color:#ae3f31;font:600 13px/1.4 ui-sans-serif,system-ui,sans-serif}
@media (max-width:820px){.login-card{grid-template-columns:1fr}.login-aside,.login-main{padding:26px}.login-aside h1{font-size:34px}}
</style>
</head>
<body>
<div class="login-shell">
	<div class="login-card">
		<div class="login-aside">
			<div class="kicker">Admin Access</div>
			<h1>Sports Stream</h1>
			<p>Sign in to reach the operational panel for stream control, analytics, notifications, and health monitoring.</p>
			<div class="login-points">
				<div class="login-point"><strong>Live analytics</strong><span>Charts update in real time over the analytics event stream.</span></div>
				<div class="login-point"><strong>Operational tooling</strong><span>Manage users, matches, streams, and outbound notifications from one console.</span></div>
			</div>
		</div>
		<div class="login-main">
			<h2>Sign In</h2>
			<p>Use the admin credentials configured for this service.</p>
			<div class="field"><label for="u">Username</label><input id="u" placeholder="Admin username" autocomplete="username"></div>
			<div class="field"><label for="p">Password</label><input id="p" type="password" placeholder="Password" autocomplete="current-password"></div>
			<div class="actions"><button onclick="login()">Open Admin Panel</button><span class="hint">Your session is stored in the secure admin cookie.</span></div>
			<div id="e" class="err"></div>
		</div>
	</div>
</div>
<script>
async function login(){var u=document.getElementById('u').value.trim();var p=document.getElementById('p').value;var r=await fetch('/admin/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:u,password:p})});var b=await r.json();if(!r.ok||!b.success){document.getElementById('e').textContent=b.message||'Login failed';return;}window.location.href='/admin';}
document.getElementById('p').addEventListener('keydown',function(e){if(e.key==='Enter')login();});
</script>
</body>
</html>`
}

func main() {
	ctx := context.Background()
	projectID := util.ProjectID()
	port := util.Getenv("PORT", "8084")
	creds := util.Getenv("FIREBASE_CREDENTIALS", "")
	envMode := strings.ToLower(util.Getenv("ENV", "development"))
	panelUser := util.Getenv("ADMIN_PANEL_USERNAME", "admin")
	panelPassword := util.Getenv("ADMIN_PANEL_PASSWORD", "")
	sessionSecret := util.Getenv("ADMIN_PANEL_SESSION_SECRET", "")

	if panelPassword == "" {
		panelPassword = "Admin@123"
		log.Println("admin: ADMIN_PANEL_PASSWORD missing; using fallback startup password")
	}

	if len(sessionSecret) < 32 {
		sessionSecret = "fallback-admin-session-secret-for-cloud-run-123456"
		log.Println("admin: ADMIN_PANEL_SESSION_SECRET missing or too short; using fallback startup secret")
	}

	if _, err := fbclient.InitClient(ctx, creds); err != nil {
		log.Fatalf("admin: firebase init: %v", err)
	}

	var fsOpts []option.ClientOption
	if util.LooksLikeJSONCredential(creds) {
		fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(creds)))
	} else if util.FileExists(creds) {
		fsOpts = append(fsOpts, option.WithCredentialsFile(creds))
	} else if creds != "" {
		log.Printf("admin-service: credential file %q not found; falling back to default credentials", creds)
	}
	fs, err := firestore.NewClient(ctx, projectID, fsOpts...)
	if err != nil {
		log.Fatalf("admin: firestore init: %v", err)
	}
	defer fs.Close()

	h := &handler{fs: fs, panelUser: panelUser, panelPassword: panelPassword, sessionSecret: sessionSecret, cookieSecure: envMode == "production"}

	r := mux.NewRouter()
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := fs.Collection("users").Limit(1).Documents(healthCtx).Next(); err != nil && err != iterator.Done {
			jsonError(w, "health check failed: firestore unreachable", http.StatusServiceUnavailable)
			return
		}
		jsonOK(w, map[string]string{"service": "admin-service", "status": "ok"})
	}).Methods(http.MethodGet)

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusFound)
	}).Methods(http.MethodGet)
	r.HandleFunc("/admin/login", h.loginPage).Methods(http.MethodGet)
	r.HandleFunc("/admin/login", h.loginSubmit).Methods(http.MethodPost)
	r.HandleFunc("/admin/logout", h.logoutSubmit).Methods(http.MethodPost)

	r.Handle("/admin", h.uiSessionRequired(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(adminPanelHTML()))
	}))).Methods(http.MethodGet)

	admin := r.PathPrefix("/api/v1/admin").Subrouter()
	admin.Use(h.adminAuthRequired)
	admin.HandleFunc("/dashboard", h.dashboard).Methods(http.MethodGet)
	admin.HandleFunc("/system/health", h.systemHealth).Methods(http.MethodGet)
	admin.HandleFunc("/notifications/send", h.sendNotification).Methods(http.MethodPost)

	admin.HandleFunc("/users", h.listUsers).Methods(http.MethodGet)
	admin.HandleFunc("/users", h.createUser).Methods(http.MethodPost)
	admin.HandleFunc("/users/{uid}", h.getUser).Methods(http.MethodGet)
	admin.HandleFunc("/users/{uid}", h.updateUser).Methods(http.MethodPatch)
	admin.HandleFunc("/users/{uid}", h.deleteUser).Methods(http.MethodDelete)
	admin.HandleFunc("/users/{uid}/role", h.updateUserRole).Methods(http.MethodPatch)

	admin.HandleFunc("/streams", h.listStreams).Methods(http.MethodGet)
	admin.HandleFunc("/streams", h.createStream).Methods(http.MethodPost)
	admin.HandleFunc("/streams/{id}", h.getStream).Methods(http.MethodGet)
	admin.HandleFunc("/streams/{id}", h.updateStream).Methods(http.MethodPatch)
	admin.HandleFunc("/streams/{id}", h.deleteStream).Methods(http.MethodDelete)
	admin.HandleFunc("/streams/{id}/end", h.endStream).Methods(http.MethodPost)
	admin.HandleFunc("/streams/{id}/reset-viewers", h.resetStreamViewers).Methods(http.MethodPost)

	admin.HandleFunc("/analytics", h.listAnalytics).Methods(http.MethodGet)
	admin.HandleFunc("/analytics", h.createAnalytics).Methods(http.MethodPost)
	admin.HandleFunc("/analytics/events", h.analyticsSSE).Methods(http.MethodGet)
	admin.HandleFunc("/analytics/top", h.topAnalytics).Methods(http.MethodGet)
	admin.HandleFunc("/analytics/{streamId}", h.getAnalytics).Methods(http.MethodGet)
	admin.HandleFunc("/analytics/{streamId}", h.updateAnalytics).Methods(http.MethodPatch)
	admin.HandleFunc("/analytics/{streamId}", h.deleteAnalytics).Methods(http.MethodDelete)

	admin.HandleFunc("/matches", h.listMatches).Methods(http.MethodGet)
	admin.HandleFunc("/matches", h.createMatch).Methods(http.MethodPost)
	admin.HandleFunc("/matches/{id}", h.getMatch).Methods(http.MethodGet)
	admin.HandleFunc("/matches/{id}", h.updateMatch).Methods(http.MethodPatch)
	admin.HandleFunc("/matches/{id}", h.deleteMatch).Methods(http.MethodDelete)

	log.Printf("admin-service listening on :%s", port)
	log.Printf("admin panel UI at /admin and API at /api/v1/admin/*")
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), r); err != nil {
		log.Fatalf("admin: ListenAndServe: %v", err)
	}
}
