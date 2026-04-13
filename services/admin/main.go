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
	var profile struct{ Role string `firestore:"role"` }
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
	var body struct{ Role string `json:"role"` }
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

func adminPanelHTML() string {
	return `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Admin</title>
<style>
body{margin:0;font-family:Palatino Linotype,serif;background:radial-gradient(circle at top,#f7f2e8 0,#efe4d3 42%,#e4d4bf 100%);color:#1f2933}
.wrap{display:grid;grid-template-columns:240px 1fr;min-height:100vh}
.side{background:linear-gradient(180deg,#2c241f 0,#3d2f29 100%);color:#f8efe0;padding:18px;border-right:1px solid rgba(255,255,255,0.08)}
.brand{font-size:12px;letter-spacing:0.22em;text-transform:uppercase;color:#d8c1a3;margin-bottom:18px}
.side button{display:block;width:100%;text-align:left;margin:8px 0;padding:10px 12px;border:1px solid rgba(255,255,255,0.08);border-radius:14px;background:rgba(255,255,255,0.04);color:#f7efe1;cursor:pointer;transition:transform .15s ease,background .15s ease,border-color .15s ease}
.side button:hover{transform:translateX(2px);background:rgba(255,255,255,0.08)}
.side button.active{background:linear-gradient(135deg,#b33a3a 0,#dd6b47 100%);border-color:transparent;box-shadow:0 12px 24px rgba(179,58,58,0.22)}
.main{padding:24px}
.card{background:rgba(255,250,242,0.84);border:1px solid rgba(102,74,48,0.14);padding:18px;border-radius:24px;box-shadow:0 18px 48px rgba(75,52,33,0.08);backdrop-filter:blur(12px)}
.toolbar{display:flex;flex-wrap:wrap;align-items:center;gap:10px;margin-bottom:16px}
.toolbar input,.toolbar button{padding:10px 12px;border:1px solid #d8c7b1;border-radius:12px;background:#fff9f1}
.toolbar button{background:linear-gradient(135deg,#b33a3a 0,#d86c43 100%);color:#fff;cursor:pointer;border:none;box-shadow:0 10px 20px rgba(179,58,58,0.18)}
.toolbar .subtle{background:#f3e9dc;color:#7f6143;box-shadow:none}
.tbl{width:100%;border-collapse:collapse;font-size:13px;background:#fffdf9;border-radius:18px;overflow:hidden}
.tbl td,.tbl th{border-bottom:1px solid #f0e4d3;padding:10px 12px;vertical-align:top}
.tbl th{background:#f9efe1;text-align:left;color:#6e5437;font-size:12px;letter-spacing:0.08em;text-transform:uppercase}
.tbl tr:hover td{background:#fff6ec}
.page-head{display:flex;justify-content:space-between;align-items:flex-start;gap:16px;margin-bottom:14px}
.page-head p{margin:6px 0 0;color:#7b6753}
.mini{font-size:12px;color:#8b7359}
.stat-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:14px;margin:16px 0 20px}
.stat-card{background:linear-gradient(180deg,#fffdf9 0,#fff3e5 100%);border:1px solid #eddcca;border-radius:18px;padding:14px 16px;box-shadow:inset 0 1px 0 rgba(255,255,255,0.8)}
.stat-label{font-size:11px;letter-spacing:0.08em;text-transform:uppercase;color:#8b7359}
.stat-value{font-size:30px;line-height:1.1;margin-top:8px;color:#2d241d}
.stat-foot{margin-top:8px;font-size:12px;color:#8a6c4f}
.analytics-layout{display:grid;grid-template-columns:minmax(0,1.45fr) minmax(280px,.85fr);gap:16px;align-items:start}
.panel{background:#fffdf9;border:1px solid #eddcca;border-radius:20px;padding:16px}
.panel h3{margin:0 0 6px;font-size:18px}
.panel p{margin:0;color:#7a6248;font-size:13px}
.leaderboard{display:grid;gap:10px;margin-top:14px}
.leader-item{padding:12px 14px;border-radius:16px;background:linear-gradient(135deg,#fff4e7 0,#fffdfb 100%);border:1px solid #f0decb}
.leader-top{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-bottom:6px}
.leader-title{font-weight:700;color:#34281e}
.leader-meta{font-size:12px;color:#8a7053}
.meter{height:10px;border-radius:999px;background:#f1e2d0;overflow:hidden;margin-top:10px}
.meter span{display:block;height:100%;border-radius:999px;background:linear-gradient(90deg,#b33a3a 0,#f0aa52 100%)}
.pill{display:inline-flex;align-items:center;gap:6px;padding:4px 10px;border-radius:999px;font-size:11px;letter-spacing:0.06em;text-transform:uppercase;background:#efe3d3;color:#7f6143}
.pill.live{background:#fee2d5;color:#a13e23}
.muted{color:#8c7358}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
.empty{padding:24px;border:1px dashed #ddcab4;border-radius:18px;background:#fffaf3;color:#85694f;text-align:center}
.analytics-table td strong{display:block;margin-bottom:4px;color:#2d241d}
.analytics-table td small{display:block;color:#8a6f53}
@media (max-width: 980px){.wrap{grid-template-columns:1fr}.side{border-right:none;border-bottom:1px solid rgba(255,255,255,0.08)}.analytics-layout{grid-template-columns:1fr}.page-head{flex-direction:column}}
</style></head><body>
<div class="wrap"><div class="side"><div class="brand">Sports Stream Admin</div><div id="menu"></div></div><div class="main"><div class="card"><h2 id="title">Dashboard</h2><div id="toolbar" class="toolbar"></div><div id="body"></div><button onclick="logout()" style="margin-top:16px">Logout</button></div></div></div>
<script>
var pages=['dashboard','users','streams','analytics','matches','notifications','health'];
var curr='dashboard';
var analyticsTimer=null;
function menu(){var m=document.getElementById('menu');var h='';for(var i=0;i<pages.length;i++){var p=pages[i];h+='<button class="'+(p===curr?'active':'')+'" onclick="go(\''+p+'\')">'+p.toUpperCase()+'</button>';}m.innerHTML=h;}
async function api(path,opts){opts=opts||{};var r=await fetch(path,{method:opts.method||'GET',headers:{'Content-Type':'application/json'},body:opts.body||undefined});var b=await r.json();if(!r.ok||!b.success)throw new Error(b.message||'failed');return b.data;}
async function safeApi(path,fallback){try{return {ok:true,data:await api(path)};}catch(e){return {ok:false,data:fallback,error:(e&&e.message)?e.message:'failed'};}}
function set(t,th,b){document.getElementById('title').textContent=t;document.getElementById('toolbar').innerHTML=th||'';document.getElementById('body').innerHTML=b||'';}
function table(headers,rows){var h='<table class="tbl"><tr>';for(var i=0;i<headers.length;i++)h+='<th>'+headers[i]+'</th>';h+='</tr>'+rows+'</table>';return h;}
function esc(v){return String(v==null?'':v).replace(/[&<>"']/g,function(ch){return({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[ch];});}
function n(v){var num=Number(v||0);return isNaN(num)?'0':num.toLocaleString();}
function fmtDate(v){if(!v)return 'No timestamp';var d=new Date(v);return isNaN(d.getTime())?String(v):d.toLocaleString();}
function setAnalyticsRefresh(enabled){if(analyticsTimer){clearInterval(analyticsTimer);analyticsTimer=null;}if(enabled){analyticsTimer=setInterval(function(){if(curr==='analytics'){renderAnalytics(true).catch(function(e){console.error(e);});}},5000);}}
async function renderDashboard(){var d=await api('/api/v1/admin/dashboard');set('Dashboard','<button onclick="renderDashboard()">Refresh</button>','Users: '+d.usersTotal+' | Admins: '+d.adminsTotal+' | Live Streams: '+d.liveStreams+' | Viewers: '+d.currentViewers+' | Total Joins: '+d.totalJoinsAll);} 
async function renderUsers(){var u=await api('/api/v1/admin/users?limit=200');var t='<input id="u_uid" placeholder="uid"><input id="u_email" placeholder="email"><input id="u_name" placeholder="displayName"><input id="u_role" placeholder="role"><button onclick="createUser()">Create</button><button onclick="renderUsers()">Refresh</button>';var rows='';for(var i=0;i<u.length;i++){var x=u[i];rows+='<tr><td>'+x.uid+'</td><td>'+ (x.displayName||'') +'</td><td>'+ (x.email||'') +'</td><td><input id="role_'+x.uid+'" value="'+(x.role||'viewer')+'"></td><td><button onclick="saveRole(\''+x.uid+'\')">Save Role</button> <button onclick="delUser(\''+x.uid+'\')">Delete</button></td></tr>';}set('Users CRUD',t,table(['UID','Name','Email','Role','Actions'],rows));}
async function createUser(){await api('/api/v1/admin/users',{method:'POST',body:JSON.stringify({uid:gid('u_uid'),email:gid('u_email'),displayName:gid('u_name'),role:gid('u_role')||'viewer'})});renderUsers();}
async function saveRole(uid){await api('/api/v1/admin/users/'+uid+'/role',{method:'PATCH',body:JSON.stringify({role:gid('role_'+uid)})});renderUsers();}
async function delUser(uid){if(!confirm('Delete user '+uid+'?'))return;await api('/api/v1/admin/users/'+uid,{method:'DELETE'});renderUsers();}
async function renderStreams(){var s=await api('/api/v1/admin/streams?limit=200');var t='<input id="s_title" placeholder="title"><input id="s_status" placeholder="status"><input id="s_uid" placeholder="broadcaster uid"><button onclick="createStream()">Create</button><button onclick="renderStreams()">Refresh</button>';var rows='';for(var i=0;i<s.length;i++){var x=s[i];rows+='<tr><td>'+x.id+'</td><td><input id="st_t_'+x.id+'" value="'+(x.title||'')+'"></td><td><input id="st_s_'+x.id+'" value="'+(x.status||'')+'"></td><td><input id="st_v_'+x.id+'" value="'+(x.viewerCount||0)+'"></td><td><button onclick="saveStream(\''+x.id+'\')">Save</button> <button onclick="endStream(\''+x.id+'\')">End</button> <button onclick="resetViewers(\''+x.id+'\')">Reset</button> <button onclick="delStream(\''+x.id+'\')">Delete</button></td></tr>';}
set('Streams CRUD',t,table(['ID','Title','Status','Viewers','Actions'],rows));}
async function createStream(){await api('/api/v1/admin/streams',{method:'POST',body:JSON.stringify({title:gid('s_title'),status:gid('s_status')||'live',broadcasterUid:gid('s_uid')})});renderStreams();}
async function saveStream(id){await api('/api/v1/admin/streams/'+id,{method:'PATCH',body:JSON.stringify({title:gid('st_t_'+id),status:gid('st_s_'+id),viewerCount:Number(gid('st_v_'+id)||0)})});renderStreams();}
async function endStream(id){await api('/api/v1/admin/streams/'+id+'/end',{method:'POST'});renderStreams();}
async function resetViewers(id){await api('/api/v1/admin/streams/'+id+'/reset-viewers',{method:'POST'});renderStreams();}
async function delStream(id){if(!confirm('Delete stream '+id+'?'))return;await api('/api/v1/admin/streams/'+id,{method:'DELETE'});renderStreams();}
async function renderAnalytics(silent){
set('Analytics Live', '<button onclick="renderAnalytics()">Refresh now</button><button class="subtle" type="button">Auto refresh every 5s</button>', '<div class="panel"><h3>Analytics Loading</h3><p>Fetching live telemetry data...</p></div>');
var res=await Promise.all([
safeApi('/api/v1/admin/analytics?limit=200',[]),
safeApi('/api/v1/admin/analytics/top?limit=5',[]),
safeApi('/api/v1/admin/streams?limit=200',[]),
safeApi('/api/v1/admin/dashboard',{})
]);
var analytics=Array.isArray(res[0].data)?res[0].data:[];
var top=Array.isArray(res[1].data)?res[1].data:[];
var streams=Array.isArray(res[2].data)?res[2].data:[];
var dashboard=res[3].data||{};
var loadWarnings=[];
for(var wi=0;wi<res.length;wi++){if(!res[wi].ok&&res[wi].error)loadWarnings.push(res[wi].error);}
var byId={};
for(var i=0;i<streams.length;i++){byId[streams[i].id]=streams[i];}
analytics.sort(function(a,b){return Number(b.currentViewers||0)-Number(a.currentViewers||0)||Number(b.peakViewers||0)-Number(a.peakViewers||0)||Number(b.totalJoins||0)-Number(a.totalJoins||0);});
var totalPeak=0,totalCurrent=0,totalJoins=0,liveTracked=0,lastUpdated='';
for(var j=0;j<analytics.length;j++){
var item=analytics[j];
totalPeak+=Number(item.peakViewers||0);
totalCurrent+=Number(item.currentViewers||0);
totalJoins+=Number(item.totalJoins||0);
if(Number(item.currentViewers||0)>0)liveTracked++;
if(item.updatedAt&&(!lastUpdated||new Date(item.updatedAt)>new Date(lastUpdated)))lastUpdated=item.updatedAt;
}
var topStream=top.length?top[0]:null;
var maxPeak=topStream?Math.max(Number(topStream.peakViewers||0),1):1;
var summary='<div class="page-head"><div><div class="mini">Live telemetry</div><p>Read-only analytics refresh automatically every 5 seconds. No manual editing on this view.</p></div><div class="pill'+(analytics.length&&totalCurrent>0?' live':'')+'">'+(analytics.length&&totalCurrent>0?'live refresh on':'watching for updates')+'</div></div>'+
'<div class="stat-grid">'+
'<div class="stat-card"><div class="stat-label">Tracked Streams</div><div class="stat-value">'+n(analytics.length)+'</div><div class="stat-foot">'+n(liveTracked)+' with active viewers</div></div>'+
'<div class="stat-card"><div class="stat-label">Current Viewers</div><div class="stat-value">'+n(totalCurrent)+'</div><div class="stat-foot">Dashboard reports '+n(dashboard.currentViewers||0)+' live viewers</div></div>'+
'<div class="stat-card"><div class="stat-label">Peak Viewers</div><div class="stat-value">'+n(totalPeak)+'</div><div class="stat-foot">Cumulative peak across tracked streams</div></div>'+
'<div class="stat-card"><div class="stat-label">Total Joins</div><div class="stat-value">'+n(totalJoins)+'</div><div class="stat-foot">Across all analytics documents</div></div>'+
'</div>';
var lead='';
if(top.length){
lead+='<div class="leaderboard">';
for(var k=0;k<top.length;k++){
var row=top[k];
var st=byId[row.streamId]||{};
var width=Math.max(8,Math.min(100,Math.round((Number(row.peakViewers||0)/maxPeak)*100)));
lead+='<div class="leader-item"><div class="leader-top"><div><div class="leader-title">'+esc(st.title||row.streamId)+'</div><div class="leader-meta mono">'+esc(row.streamId)+'</div></div><div class="pill'+((st.status||'')==='live'?' live':'')+'">'+esc(st.status||'tracked')+'</div></div><div class="leader-meta">Peak '+n(row.peakViewers)+' · Current '+n(row.currentViewers)+' · Joins '+n(row.totalJoins)+'</div><div class="meter"><span style="width:'+width+'%"></span></div></div>';
}
lead+='</div>';
} else {
lead='<div class="empty">No analytics documents yet. Once viewers join streams, live telemetry will appear here automatically.</div>';
}
var rows='';
for(var m=0;m<analytics.length;m++){
var a=analytics[m];
var s=byId[a.streamId]||{};
var engagement=Number(a.peakViewers||0)>0?Math.round((Number(a.currentViewers||0)/Math.max(Number(a.peakViewers||0),1))*100):0;
rows+='<tr><td><strong>'+esc(s.title||a.streamId)+'</strong><small class="mono">'+esc(a.streamId)+'</small></td><td><span class="pill'+((s.status||'')==='live'?' live':'')+'">'+esc(s.status||'unknown')+'</span><small>'+(s.broadcasterUid?esc(s.broadcasterUid):'No broadcaster')+'</small></td><td><strong>'+n(a.currentViewers)+'</strong><small>Live now</small></td><td><strong>'+n(a.peakViewers)+'</strong><small>Best moment</small></td><td><strong>'+n(a.totalJoins)+'</strong><small>Cumulative joins</small></td><td><strong>'+n(engagement)+'%</strong><small>Current vs peak</small></td><td><strong>'+fmtDate(a.updatedAt)+'</strong><small>Last event</small></td></tr>';
}
var detail=analytics.length?table(['Stream','Status','Current','Peak','Joins','Engagement','Updated'],rows):'<div class="empty">Waiting for analytics data.</div>';
var warn='';
if(loadWarnings.length){warn='<div class="panel" style="margin-bottom:14px"><h3>Partial Data Mode</h3><p class="muted">Some analytics sources are unavailable right now, but the dashboard is still visible. Last error: '+esc(loadWarnings[0])+'</p></div>';}
var body=warn+summary+'<div class="analytics-layout"><div><div class="panel"><h3>Detailed Stream Telemetry</h3><p>Viewer counts, peak load, join volume, engagement ratio, and last event time for each tracked stream.</p><div style="margin-top:14px" class="analytics-table">'+detail+'</div></div></div><div><div class="panel"><h3>Top Performing Streams</h3><p>Streams ranked by peak viewers with live context from the stream service.</p>'+lead+'</div><div class="panel" style="margin-top:16px"><h3>Feed Status</h3><p>Last refresh: '+fmtDate(new Date().toISOString())+'</p><div class="stat-grid" style="margin-top:14px"><div class="stat-card"><div class="stat-label">Latest Event</div><div class="stat-value" style="font-size:20px">'+esc(fmtDate(lastUpdated))+'</div><div class="stat-foot">Newest analytics update timestamp</div></div><div class="stat-card"><div class="stat-label">Live Streams</div><div class="stat-value" style="font-size:20px">'+n(dashboard.liveStreams||0)+'</div><div class="stat-foot">From dashboard aggregate</div></div></div></div></div></div>';
set('Analytics Live', '<button onclick="renderAnalytics()">Refresh now</button><button class="subtle" type="button">Auto refresh every 5s</button>', body);
setAnalyticsRefresh(true);
if(!silent){window.scrollTo({top:0,behavior:'smooth'});}
}
async function renderMatches(){var m=await api('/api/v1/admin/matches?limit=200');var t='<input id="m_t" placeholder="title"><input id="m_at" placeholder="scheduledAt RFC3339"><input id="m_s" placeholder="status"><button onclick="createMatch()">Create</button><button onclick="renderMatches()">Refresh</button>';var rows='';for(var i=0;i<m.length;i++){var x=m[i];rows+='<tr><td>'+x.id+'</td><td><input id="mt_'+x.id+'" value="'+(x.title||'')+'"></td><td><input id="ma_'+x.id+'" value="'+(x.scheduledAt||'')+'"></td><td><input id="ms_'+x.id+'" value="'+(x.status||'scheduled')+'"></td><td><button onclick="saveMatch(\''+x.id+'\')">Save</button> <button onclick="delMatch(\''+x.id+'\')">Delete</button></td></tr>';}
set('Matches CRUD',t,table(['ID','Title','ScheduledAt','Status','Actions'],rows));}
async function createMatch(){await api('/api/v1/admin/matches',{method:'POST',body:JSON.stringify({title:gid('m_t'),scheduledAt:gid('m_at'),status:gid('m_s')||'scheduled'})});renderMatches();}
async function saveMatch(id){await api('/api/v1/admin/matches/'+id,{method:'PATCH',body:JSON.stringify({title:gid('mt_'+id),scheduledAt:gid('ma_'+id),status:gid('ms_'+id)})});renderMatches();}
async function delMatch(id){if(!confirm('Delete match '+id+'?'))return;await api('/api/v1/admin/matches/'+id,{method:'DELETE'});renderMatches();}
async function renderNotifications(){set('Notifications','<input id="n_e" placeholder="eventType"><input id="n_id" placeholder="streamId"><input id="n_t" placeholder="title"><button onclick="sendNotification()">Send</button>','<pre id="n_out"></pre>');}
async function sendNotification(){var d=await api('/api/v1/admin/notifications/send',{method:'POST',body:JSON.stringify({eventType:gid('n_e'),streamId:gid('n_id'),title:gid('n_t')})});document.getElementById('n_out').textContent=JSON.stringify(d,null,2);} 
async function renderHealth(){var h=await api('/api/v1/admin/system/health');var rows='';for(var i=0;i<h.length;i++){rows+='<tr><td>'+h[i].name+'</td><td>'+h[i].status+'</td><td>'+h[i].url+'</td><td>'+(h[i].details||'')+'</td></tr>';}set('System Health','<button onclick="renderHealth()">Refresh</button>',table(['Service','Status','URL','Details'],rows));}
function gid(id){var e=document.getElementById(id);return e?e.value.trim():'';}
function go(p){setAnalyticsRefresh(false);curr=p;menu();var m={dashboard:renderDashboard,users:renderUsers,streams:renderStreams,analytics:renderAnalytics,matches:renderMatches,notifications:renderNotifications,health:renderHealth};m[p]().catch(function(e){alert(e.message);});}
async function logout(){await fetch('/admin/logout',{method:'POST'});window.location.href='/admin/login';}
menu();go('dashboard');
</script></body></html>`
}

func loginHTML() string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Admin Login</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f3efe7;font-family:Palatino Linotype,serif} .card{width:100%;max-width:420px;background:#fff9f0;border:1px solid #dacfbf;padding:18px} input,button{width:100%;padding:10px;margin-bottom:8px;border:1px solid #dacfbf} button{background:#b33a3a;color:#fff;cursor:pointer} .err{color:#9f2f2f;min-height:18px}</style>
</head><body><div class="card"><h2>Admin Login</h2><p>Sign in to access admin panel.</p><input id="u" placeholder="Username"><input id="p" type="password" placeholder="Password"><button onclick="login()">Sign In</button><div id="e" class="err"></div></div>
<script>
async function login(){var u=document.getElementById('u').value.trim();var p=document.getElementById('p').value;var r=await fetch('/admin/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:u,password:p})});var b=await r.json();if(!r.ok||!b.success){document.getElementById('e').textContent=b.message||'Login failed';return;}window.location.href='/admin';}
</script></body></html>`
}

func main() {
	ctx := context.Background()
	projectID := util.MustGetenv("GCP_PROJECT_ID")
	port := util.Getenv("PORT", "8084")
	creds := util.Getenv("FIREBASE_CREDENTIALS", "")
	envMode := strings.ToLower(util.Getenv("ENV", "development"))
	panelUser := util.Getenv("ADMIN_PANEL_USERNAME", "admin")
	panelPassword := util.Getenv("ADMIN_PANEL_PASSWORD", "")
	sessionSecret := util.Getenv("ADMIN_PANEL_SESSION_SECRET", "")

	if panelPassword == "" {
		if envMode == "production" {
			log.Fatal("admin: ADMIN_PANEL_PASSWORD is required in production")
		}
		panelPassword = "admin"
		log.Println("admin: using development fallback ADMIN_PANEL_PASSWORD")
	}

	if len(sessionSecret) < 32 {
		if envMode == "production" {
			log.Fatal("admin: ADMIN_PANEL_SESSION_SECRET must be set and at least 32 chars in production")
		}
		sessionSecret = "dev-admin-session-secret-change-me-please"
		log.Println("admin: using development fallback ADMIN_PANEL_SESSION_SECRET")
	}

	if _, err := fbclient.InitClient(ctx, creds); err != nil {
		log.Fatalf("admin: firebase init: %v", err)
	}

	var fsOpts []option.ClientOption
	if strings.HasPrefix(strings.TrimSpace(creds), "{") {
		fsOpts = append(fsOpts, option.WithCredentialsJSON([]byte(creds)))
	} else if creds != "" {
		fsOpts = append(fsOpts, option.WithCredentialsFile(creds))
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
	admin.HandleFunc("/analytics/{streamId}", h.getAnalytics).Methods(http.MethodGet)
	admin.HandleFunc("/analytics/{streamId}", h.updateAnalytics).Methods(http.MethodPatch)
	admin.HandleFunc("/analytics/{streamId}", h.deleteAnalytics).Methods(http.MethodDelete)
	admin.HandleFunc("/analytics/top", h.topAnalytics).Methods(http.MethodGet)

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
