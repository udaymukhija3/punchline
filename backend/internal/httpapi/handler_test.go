package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"punchline/backend/internal/cards"
	"punchline/backend/internal/realtime"
)

func TestJoinReturnsGuestTokenAndSnapshotsDoNotLeakIt(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	server := httptest.NewServer(handler.Routes())
	defer server.Close()

	createResp, err := http.Post(server.URL+"/api/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer createResp.Body.Close()
	var created struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Code == "" {
		t.Fatal("create room response did not include a room code")
	}

	body := bytes.NewBufferString(`{"name":"Alice"}`)
	joinResp, err := http.Post(server.URL+"/api/rooms/"+created.Code+"/join", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer joinResp.Body.Close()
	if joinResp.StatusCode != http.StatusCreated {
		t.Fatalf("join status = %d, want %d", joinResp.StatusCode, http.StatusCreated)
	}
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(joinResp.Body); err != nil {
		t.Fatal(err)
	}
	var joined struct {
		Token  string `json:"token"`
		Player any    `json:"player"`
		Room   any    `json:"room"`
	}
	if err := json.Unmarshal(raw.Bytes(), &joined); err != nil {
		t.Fatal(err)
	}
	if joined.Token == "" {
		t.Fatal("join response did not include guest token")
	}
	for label, payload := range map[string]any{"player": joined.Player, "room": joined.Room} {
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(joined.Token)) {
			t.Fatalf("%s payload leaked guest token", label)
		}
	}
}

func TestWebSocketEndpointRequiresGuestToken(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws/rooms/ABCD?player_id=pl_123", nil)

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSecurityHeadersAndSameOriginCORS(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "punchline.example"
	req.Header.Set("Origin", "http://punchline.example")

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://punchline.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Permissions-Policy"} {
		if rec.Header().Get(name) == "" {
			t.Fatalf("missing security header %s", name)
		}
	}
}

func TestDisallowedOriginIsRejected(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/rooms", nil)
	req.Host = "punchline.example"
	req.Header.Set("Origin", "https://evil.example")

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestJoinRejectsInvalidJSON(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	server := httptest.NewServer(handler.Routes())
	defer server.Close()

	createResp, err := http.Post(server.URL+"/api/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer createResp.Body.Close()
	var created struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	joinResp, err := http.Post(server.URL+"/api/rooms/"+created.Code+"/join", "application/json", bytes.NewBufferString(`{`))
	if err != nil {
		t.Fatal(err)
	}
	defer joinResp.Body.Close()
	if joinResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("join status = %d, want %d", joinResp.StatusCode, http.StatusBadRequest)
	}
}

func TestJoinAfterGameStartsIsRejected(t *testing.T) {
	manager := realtime.NewRoomManager(cards.NewSeedDeck())
	room, err := manager.CreateRoom(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	host, err := room.TryJoin("Host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := room.TryJoin("Bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := room.TryJoin("Carol"); err != nil {
		t.Fatal(err)
	}
	if err := room.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(manager)
	body := bytes.NewBufferString(`{"name":"Late"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.SnapshotFor("").Code+"/join", body)
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}
