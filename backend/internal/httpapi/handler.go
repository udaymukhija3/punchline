package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"punchline/backend/internal/realtime"
	"punchline/backend/internal/ws"
)

// maxJoinBody bounds the join request body to defend against oversized payloads.
const maxJoinBody = 4 << 10

type Handler struct {
	manager        *realtime.RoomManager
	allowedOrigins map[string]bool
}

var errUnknownMessage = errors.New("unknown websocket message type")

func NewHandler(manager *realtime.RoomManager) *Handler {
	return &Handler{manager: manager, allowedOrigins: parseAllowedOrigins(os.Getenv("PUNCHLINE_ALLOWED_ORIGINS"))}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.health)
	mux.HandleFunc("/readyz", h.ready)
	mux.HandleFunc("/api/rooms", h.rooms)
	mux.HandleFunc("/api/rooms/", h.roomByCode)
	mux.HandleFunc("/ws/rooms/", h.wsRoom)
	if dir := staticDir(); dir != "" {
		mux.Handle("/", spaHandler(dir))
	}
	return h.securityHeaders(h.cors(mux))
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	stats := h.manager.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"instance_id":      stats.InstanceID,
		"room_registry":    stats.RoomRegistry,
		"local_room_count": stats.LocalRoomCount,
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

func (h *Handler) rooms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	room, err := h.manager.CreateRoom(r.Context())
	if err != nil {
		log.Printf("create room: %v", err)
		writeError(w, http.StatusServiceUnavailable, "could not create room")
		return
	}
	writeJSON(w, http.StatusCreated, room.SnapshotFor(""))
}

func (h *Handler) roomByCode(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/rooms/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "room code required")
		return
	}
	code := strings.ToUpper(parts[0])
	room, err := h.manager.GetRoom(r.Context(), code)
	if err != nil {
		writeRoomLookupError(w, r, err)
		return
	}

	if len(parts) == 1 && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, room.SnapshotFor(""))
		return
	}
	if len(parts) == 2 && parts[1] == "join" && r.Method == http.MethodPost {
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
	defer func() { room.Detach(playerID, conn); room.Broadcast() }()

	// The connection's write pump emits keepalive pings on its own, so we just
	// read client messages here and let broadcasts fan out asynchronously.
	room.Broadcast()
	for {
		var msg realtime.ClientMessage
		if err := conn.ReadJSON(&msg); err != nil {
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
		default:
			actionErr = errUnknownMessage
		}
		if actionErr != nil {
			_ = conn.WriteJSON(realtime.ServerMessage{Type: "error", Room: room.SnapshotFor(playerID), Error: actionErr.Error()})
			continue
		}
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
	return sameHost(u.Host, r.Host)
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
