package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"punchline/backend/internal/realtime"
	"punchline/backend/internal/ws"
)

// maxJoinBody bounds the join request body to defend against oversized payloads.
const maxJoinBody = 4 << 10

type Handler struct {
	manager               *realtime.RoomManager
	allowedOrigins        map[string]bool
	metrics               *metrics
	metricsToken          string
	limiter               *rateLimiter
	proxyHeaders          proxyHeaderConfig
	roomCreateLimitPerMin int
	roomJoinLimitPerMin   int
	wsConnectLimitPerMin  int
	wsMessageLimitPerMin  int
}

var errUnknownMessage = errors.New("unknown websocket message type")

func NewHandler(manager *realtime.RoomManager) *Handler {
	return &Handler{
		manager:               manager,
		allowedOrigins:        parseAllowedOrigins(os.Getenv("PUNCHLINE_ALLOWED_ORIGINS")),
		metrics:               newMetrics(),
		metricsToken:          strings.TrimSpace(os.Getenv("PUNCHLINE_METRICS_TOKEN")),
		limiter:               newRateLimiter(),
		proxyHeaders:          newProxyHeaderConfig(),
		roomCreateLimitPerMin: getenvLimit("PUNCHLINE_ROOM_CREATE_LIMIT_PER_MIN", defaultRoomCreateLimitPerMin),
		roomJoinLimitPerMin:   getenvLimit("PUNCHLINE_ROOM_JOIN_LIMIT_PER_MIN", defaultRoomJoinLimitPerMin),
		wsConnectLimitPerMin:  getenvLimit("PUNCHLINE_WS_CONNECT_LIMIT_PER_MIN", defaultWSConnectLimitPerMin),
		wsMessageLimitPerMin:  getenvLimit("PUNCHLINE_WS_MESSAGE_LIMIT_PER_MIN", defaultWSMessageLimitPerMin),
	}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.health)
	mux.HandleFunc("/readyz", h.ready)
	mux.HandleFunc("/metrics", h.metricsEndpoint)
	mux.HandleFunc("/api/computer-room", h.computerRoom)
	mux.HandleFunc("/api/rooms", h.rooms)
	mux.HandleFunc("/api/rooms/", h.roomByCode)
	mux.HandleFunc("/ws/rooms/", h.wsRoom)
	if dir := staticDir(); dir != "" {
		mux.Handle("/", spaHandler(dir))
	}
	return h.instrument(h.securityHeaders(h.cors(mux)))
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	stats := h.manager.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"instance_id":      stats.InstanceID,
		"room_registry":    stats.RoomRegistry,
		"room_state_store": stats.RoomStateStore,
		"local_room_count": stats.LocalRoomCount,
		"draining":         stats.Draining,
	})
}

// ready is a readiness probe: it fails when a backing dependency (e.g. the
// Postgres room registry) is unreachable, so the platform can route traffic
// away from this instance without killing it.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	if err := h.manager.Ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true})
}

func (h *Handler) metricsEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.metricsToken != "" && !constantTimeEqual(r.Header.Get("Authorization"), "Bearer "+h.metricsToken) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "metrics authorization required")
		return
	}
	h.metrics.writePrometheus(w, h.manager.Stats())
}

func (h *Handler) rooms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowRequest(w, r, "room_create", h.roomCreateLimitPerMin) {
		return
	}
	room, err := h.manager.CreateRoom(r.Context())
	if err != nil {
		log.Printf("create room: %v", err)
		writeError(w, http.StatusServiceUnavailable, "could not create room")
		return
	}
	h.metrics.recordRoomCreated("friends")
	writeJSON(w, http.StatusCreated, room.SnapshotFor(""))
}

func (h *Handler) computerRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowRequest(w, r, "room_create", h.roomCreateLimitPerMin) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJoinBody)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid computer room request")
		return
	}
	room, err := h.manager.CreateRoom(r.Context())
	if err != nil {
		log.Printf("create computer room: %v", err)
		writeError(w, http.StatusServiceUnavailable, "could not create room")
		return
	}
	player, err := room.TryJoin(req.Name)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not join room")
		return
	}
	if err := room.UpdateSettings(player.ID, &realtime.Settings{ScoreLimit: 3, RoundSeconds: 45, MaxPlayers: 4}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not prepare computer room")
		return
	}
	if err := room.StartComputerGame(player.ID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not start computer room")
		return
	}
	if err := h.manager.PersistRoom(r.Context(), room); err != nil {
		log.Printf("persist computer room: %v", err)
		writeError(w, http.StatusServiceUnavailable, "could not save computer room")
		return
	}
	h.metrics.recordRoomCreated("computer")
	writeJSON(w, http.StatusCreated, map[string]any{"player": player, "token": player.GuestToken, "room": room.SnapshotFor(player.ID)})
}

func (h *Handler) roomByCode(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/rooms/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "room code required")
		return
	}
	code := strings.ToUpper(parts[0])
	isJoin := len(parts) == 2 && parts[1] == "join" && r.Method == http.MethodPost
	if isJoin && !h.allowRequest(w, r, "room_join", h.roomJoinLimitPerMin) {
		return
	}
	room, err := h.manager.GetRoom(r.Context(), code)
	if err != nil {
		writeRoomLookupError(w, r, err)
		return
	}

	if len(parts) == 1 && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, room.SnapshotFor(""))
		return
	}
	if isJoin {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJoinBody)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid join request")
			return
		}
		player, err := room.TryJoin(req.Name)
		if err != nil {
			if errors.Is(err, realtime.ErrRoomFull) {
				writeError(w, http.StatusConflict, "room is full")
				return
			}
			if errors.Is(err, realtime.ErrRoomAlreadyStarted) {
				writeError(w, http.StatusConflict, "game already started")
				return
			}
			writeError(w, http.StatusServiceUnavailable, "could not join room")
			return
		}
		if err := h.manager.PersistRoom(r.Context(), room); err != nil {
			log.Printf("persist joined room: %v", err)
			writeError(w, http.StatusServiceUnavailable, "could not save room")
			return
		}
		room.Broadcast()
		writeJSON(w, http.StatusCreated, map[string]any{"player": player, "token": player.GuestToken, "room": room.SnapshotFor(player.ID)})
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

func (h *Handler) wsRoom(w http.ResponseWriter, r *http.Request) {
	if !h.originAllowed(r) {
		writeError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	if !h.allowRequest(w, r, "ws_connect", h.wsConnectLimitPerMin) {
		return
	}
	code := strings.ToUpper(strings.Trim(strings.TrimPrefix(r.URL.Path, "/ws/rooms/"), "/"))
	playerID := r.URL.Query().Get("player_id")
	guestToken := r.URL.Query().Get("token")
	if code == "" || playerID == "" || guestToken == "" {
		writeError(w, http.StatusBadRequest, "room code, player_id, and token are required")
		return
	}
	room, err := h.manager.GetRoom(r.Context(), code)
	if err != nil {
		writeRoomLookupError(w, r, err)
		return
	}
	conn, err := ws.Upgrade(w, r)
	if err != nil {
		log.Println("ws upgrade:", err)
		return
	}
	defer conn.Close()

	if err := room.Attach(playerID, guestToken, conn); err != nil {
		_ = conn.WriteJSON(realtime.ServerMessage{Type: "error", Error: err.Error()})
		return
	}
	h.metrics.recordWSConnect()
	defer func() {
		h.metrics.recordWSDisconnect()
		room.Detach(playerID, conn)
		room.Broadcast()
	}()

	// The connection's write pump emits keepalive pings on its own, so we just
	// read client messages here and let broadcasts fan out asynchronously.
	room.Broadcast()
	for {
		var msg realtime.ClientMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if h.manager.Draining() {
			_ = conn.WriteJSON(realtime.ServerMessage{Type: "server_draining", Room: room.SnapshotFor(playerID), Error: "server is restarting; reconnect shortly"})
			return
		}
		if !h.limiter.allow("ws_message:"+h.clientIP(r)+":"+playerID, h.wsMessageLimitPerMin, time.Minute) {
			h.metrics.recordRateLimited("ws_message")
			h.metrics.recordWSMessage(msg.Type, "rate_limited")
			_ = conn.WriteJSON(realtime.ServerMessage{Type: "error", Room: room.SnapshotFor(playerID), Error: "too many messages"})
			return
		}
		var actionErr error
		switch msg.Type {
		case "start_game":
			actionErr = room.StartGame(playerID)
		case "submit_answer":
			actionErr = room.SubmitAnswer(playerID, msg.AnswerCardID)
		case "pick_winner":
			actionErr = room.PickWinner(playerID, msg.SubmissionID)
		case "next_round":
			actionErr = room.NextRound(playerID)
		case "end_game":
			actionErr = room.EndGame(playerID)
		case "play_again":
			actionErr = room.PlayAgain(playerID)
		case "update_settings":
			actionErr = room.UpdateSettings(playerID, msg.Settings)
		case "skip_prompt":
			actionErr = room.SkipPrompt(playerID)
		case "start_computer_game":
			actionErr = room.StartComputerGame(playerID)
		default:
			actionErr = errUnknownMessage
		}
		if actionErr != nil {
			h.metrics.recordActionError(msg.Type)
			h.metrics.recordWSMessage(msg.Type, "error")
			_ = conn.WriteJSON(realtime.ServerMessage{Type: "error", Room: room.SnapshotFor(playerID), Error: actionErr.Error()})
			continue
		}
		if err := h.manager.PersistRoom(r.Context(), room); err != nil {
			log.Printf("persist room action %s: %v", msg.Type, err)
			h.metrics.recordActionError(msg.Type)
			h.metrics.recordWSMessage(msg.Type, "error")
			_ = conn.WriteJSON(realtime.ServerMessage{Type: "error", Room: room.SnapshotFor(playerID), Error: "could not save room state"})
			room.Broadcast()
			continue
		}
		h.metrics.recordWSMessage(msg.Type, "ok")
		room.Broadcast()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (h *Handler) allowRequest(w http.ResponseWriter, r *http.Request, scope string, limit int) bool {
	if h.limiter.allow(scope+":"+h.clientIP(r), limit, time.Minute) {
		return true
	}
	h.metrics.recordRateLimited(scope)
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "too many requests")
	return false
}

func (h *Handler) clientIP(r *http.Request) string {
	return h.proxyHeaders.clientIP(r)
}

func writeRoomLookupError(w http.ResponseWriter, r *http.Request, err error) {
	var ownedElsewhere *realtime.RoomOwnedElsewhereError
	if errors.As(err, &ownedElsewhere) {
		w.Header().Set("X-Punchline-Room-Instance", ownedElsewhere.InstanceID)
		if r.Header.Get("Fly-Replay-Failed") == "" {
			w.Header().Set("Fly-Replay", "instance="+ownedElsewhere.InstanceID)
		}
		writeError(w, http.StatusMisdirectedRequest, "room is hosted by another server instance")
		return
	}
	if errors.Is(err, realtime.ErrRoomStateUnavailable) {
		writeError(w, http.StatusGone, "room state is no longer active on this server")
		return
	}
	if errors.Is(err, realtime.ErrDraining) {
		writeError(w, http.StatusServiceUnavailable, "server is restarting; reconnect shortly")
		return
	}
	if errors.Is(err, realtime.ErrRoomNotFound) {
		writeError(w, http.StatusNotFound, "room not found")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "room lookup failed")
}

func (h *Handler) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if !h.originAllowed(r) {
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if h.allowedOrigins[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return sameHost(u.Host, r.Host) || sameLoopbackHost(u.Host, r.Host)
}

func parseAllowedOrigins(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(part)
		if origin != "" {
			out[origin] = true
		}
	}
	return out
}

func sameHost(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	ah, ap, aerr := net.SplitHostPort(a)
	bh, bp, berr := net.SplitHostPort(b)
	if aerr == nil && berr == nil {
		return strings.EqualFold(ah, bh) && ap == bp
	}
	return false
}

func sameLoopbackHost(a, b string) bool {
	ah, _, aerr := net.SplitHostPort(a)
	if aerr != nil {
		ah = a
	}
	bh, _, berr := net.SplitHostPort(b)
	if berr != nil {
		bh = b
	}
	return isLoopbackHost(ah) && isLoopbackHost(bh)
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func constantTimeEqual(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
