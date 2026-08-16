package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRoomOwnedElsewhereSetsFlyReplayHeader(t *testing.T) {
	ctx := context.Background()
	registry := realtime.NewMemoryRoomRegistry()
	owner := realtime.NewRoomManager(
		cards.NewSeedDeck(),
		realtime.WithRoomRegistry(registry),
		realtime.WithInstanceID("owner-machine"),
	)
	room, err := owner.CreateRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other := realtime.NewRoomManager(
		cards.NewSeedDeck(),
		realtime.WithRoomRegistry(registry),
		realtime.WithInstanceID("other-machine"),
	)
	handler := NewHandler(other)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/rooms/"+room.SnapshotFor("").Code, nil)

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMisdirectedRequest)
	}
	if got := rec.Header().Get("Fly-Replay"); got != "instance=owner-machine" {
		t.Fatalf("Fly-Replay = %q, want owner instance", got)
	}
	if got := rec.Header().Get("X-Punchline-Room-Instance"); got != "owner-machine" {
		t.Fatalf("X-Punchline-Room-Instance = %q, want owner-machine", got)
	}
}

func TestComputerRoomStartsWithComputerPlayers(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/computer-room", bytes.NewBufferString(`{"name":"Alice"}`))

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	var payload struct {
		Token string `json:"token"`
		Room  struct {
			Phase   realtime.Phase    `json:"phase"`
			JudgeID string            `json:"judge_id"`
			Players []realtime.Player `json:"players"`
			Prompt  *cards.PromptCard `json:"prompt"`
		} `json:"room"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Token == "" {
		t.Fatal("computer room did not return a guest token")
	}
	if payload.Room.Phase != realtime.PhaseSubmitting {
		t.Fatalf("phase = %q, want submitting", payload.Room.Phase)
	}
	if payload.Room.Prompt == nil {
		t.Fatal("started computer room did not include a prompt")
	}
	computers := 0
	for _, p := range payload.Room.Players {
		if p.IsComputer {
			computers++
		}
	}
	if computers != 2 {
		t.Fatalf("computer players = %d, want 2", computers)
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
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' ws://punchline.example wss://punchline.example;") {
		t.Fatalf("CSP connect-src is not pinned to the request host: %q", csp)
	}
	if strings.Contains(csp, "ws: ") || strings.Contains(csp, " wss:;") {
		t.Fatalf("CSP connect-src allows arbitrary websocket hosts: %q", csp)
	}
}

func TestLoopbackDevOriginIsAllowedAcrossPorts(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/computer-room", bytes.NewBufferString(`{"name":"Alice"}`))
	req.Host = "localhost:8080"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Content-Type", "application/json")

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
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

func TestJoinAfterGameStartsIsAllowed(t *testing.T) {
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

	// Someone clicking the invite link after the host started is the normal
	// case at a party, not an error.
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := len(room.SnapshotFor("").Players); got != 4 {
		t.Fatalf("roster = %d, want the latecomer seated", got)
	}
}

func TestMetricsEndpointReportsRequestsAndRoomGauge(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	server := httptest.NewServer(handler.Routes())
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/rooms", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	metricsResp, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer metricsResp.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(metricsResp.Body); err != nil {
		t.Fatal(err)
	}
	text := body.String()
	for _, want := range []string{
		"punchline_http_requests_total",
		`route="/api/rooms"`,
		`status="201"`,
		"punchline_rooms_created_total",
		`mode="friends"`,
		"punchline_rooms_local",
		"punchline_instance_draining",
		"punchline_registry_operations_total",
		`operation="reserve_room"`,
		"punchline_go_heap_alloc_bytes",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics did not contain %q:\n%s", want, text)
		}
	}
}

func TestRoomCreateRateLimit(t *testing.T) {
	t.Setenv("PUNCHLINE_ROOM_CREATE_LIMIT_PER_MIN", "1")
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))

	req1 := httptest.NewRequest(http.MethodPost, "/api/rooms", nil)
	req1.RemoteAddr = "203.0.113.8:1000"
	rec1 := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusCreated)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/rooms", nil)
	req2.RemoteAddr = "203.0.113.8:2000"
	rec2 := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
	if got := rec2.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec3 := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec3, req3)
	if !strings.Contains(rec3.Body.String(), `punchline_rate_limited_total{scope="room_create"} 1`) {
		t.Fatalf("rate limit metric missing:\n%s", rec3.Body.String())
	}
}

func TestRateLimitIgnoresForwardedIPHeadersByDefault(t *testing.T) {
	t.Setenv("PUNCHLINE_ROOM_CREATE_LIMIT_PER_MIN", "1")
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))

	req1 := httptest.NewRequest(http.MethodPost, "/api/rooms", nil)
	req1.RemoteAddr = "203.0.113.8:1000"
	req1.Header.Set("X-Forwarded-For", "198.51.100.10")
	rec1 := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusCreated)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/rooms", nil)
	req2.RemoteAddr = "203.0.113.8:2000"
	req2.Header.Set("X-Forwarded-For", "198.51.100.11")
	rec2 := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
}

func TestRateLimitCanTrustConfiguredProxyHeaders(t *testing.T) {
	t.Setenv("PUNCHLINE_ROOM_CREATE_LIMIT_PER_MIN", "1")
	t.Setenv("PUNCHLINE_TRUSTED_PROXY_CIDRS", "203.0.113.0/24")
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))

	req1 := httptest.NewRequest(http.MethodPost, "/api/rooms", nil)
	req1.RemoteAddr = "203.0.113.8:1000"
	req1.Header.Set("X-Forwarded-For", "198.51.100.10")
	rec1 := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusCreated)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/rooms", nil)
	req2.RemoteAddr = "203.0.113.8:2000"
	req2.Header.Set("X-Forwarded-For", "198.51.100.11")
	rec2 := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusCreated)
	}
}

func TestMetricsTokenProtectsEndpointWhenConfigured(t *testing.T) {
	t.Setenv("PUNCHLINE_METRICS_TOKEN", "metrics-test-token")
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))

	unauthorized := httptest.NewRecorder()
	handler.Routes().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer metrics-test-token")
	authorized := httptest.NewRecorder()
	handler.Routes().ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d", authorized.Code, http.StatusOK)
	}
}

func TestDailyModeFailsClosedWithoutPostgres(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	body := bytes.NewBufferString(`{"name":"Daily friends","player_name":"Sam","timezone":"UTC"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/daily/groups", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "requires PostgreSQL") {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
}

func TestDailyCORSAllowsBearerAuthorization(t *testing.T) {
	handler := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	req := httptest.NewRequest(http.MethodOptions, "/api/daily/groups/ABC234/today", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
		t.Fatalf("Access-Control-Allow-Headers = %q", got)
	}
}

func TestAdminRoutesAreInvisibleWithoutAToken(t *testing.T) {
	t.Setenv("PUNCHLINE_ADMIN_TOKEN", "")
	h := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	for _, path := range []string{"/api/admin/overview", "/api/admin/reports", "/api/admin/candidates", "/api/admin/cards"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		// 404, not 401: an unconfigured desk should not advertise that it exists.
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s without a token = %d, want 404", path, res.StatusCode)
		}
	}
}

func TestAdminAuthFailuresAreRateLimited(t *testing.T) {
	t.Setenv("PUNCHLINE_ADMIN_TOKEN", "the-real-token")
	t.Setenv("PUNCHLINE_ADMIN_AUTH_FAILURE_LIMIT_PER_MIN", "3")
	h := NewHandler(realtime.NewRoomManager(cards.NewSeedDeck()))
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	statuses := []int{}
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/admin/overview", nil)
		req.Header.Set("Authorization", "Bearer wrong-guess")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		statuses = append(statuses, res.StatusCode)
	}
	if statuses[0] != http.StatusUnauthorized || statuses[2] != http.StatusUnauthorized {
		t.Fatalf("early guesses = %v, want 401s", statuses)
	}
	if statuses[3] != http.StatusTooManyRequests || statuses[4] != http.StatusTooManyRequests {
		t.Fatalf("guesses past the limit = %v, want 429s", statuses)
	}
}

// A name the room cannot persist is a bad request, not a server fault, and it
// must not disturb the room. Before this was fixed, the join was accepted into
// the roster and the room's Postgres snapshot broke permanently: every later
// join returned 503 and every later action returned "could not save room
// state" for everyone already playing.
func TestJoinWithControlCharactersIsRejectedAndLeavesTheRoomHealthy(t *testing.T) {
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

	join := func(name string) int {
		payload, err := json.Marshal(map[string]string{"name": name})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(server.URL+"/api/rooms/"+created.Code+"/join", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := join("Victim"); got != http.StatusCreated {
		t.Fatalf("first join status = %d, want %d", got, http.StatusCreated)
	}
	if got := join("Attacker\x00x"); got != http.StatusBadRequest {
		t.Fatalf("control-character join status = %d, want %d", got, http.StatusBadRequest)
	}
	// The room used to be dead from here on.
	if got := join("Carol"); got != http.StatusCreated {
		t.Fatalf("join after rejected name = %d, want %d", got, http.StatusCreated)
	}

	roomResp, err := http.Get(server.URL + "/api/rooms/" + created.Code)
	if err != nil {
		t.Fatal(err)
	}
	defer roomResp.Body.Close()
	var snapshot struct {
		Players []struct {
			Name string `json:"name"`
		} `json:"players"`
	}
	if err := json.NewDecoder(roomResp.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Players) != 2 {
		t.Fatalf("roster = %+v, want exactly Victim and Carol", snapshot.Players)
	}
	for _, p := range snapshot.Players {
		if strings.ContainsRune(p.Name, 0) {
			t.Fatalf("roster kept an unpersistable name: %q", p.Name)
		}
	}
}
