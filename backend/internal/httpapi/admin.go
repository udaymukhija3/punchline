package httpapi

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"punchline/backend/internal/content"
)

// The admin desk is protected by a single shared token, matching how the
// metrics endpoint is already secured. The product has no user accounts, so
// there is nothing to build a role system on top of; a token in the deploy's
// secret store is the honest amount of auth for a one-operator desk.
//
// Fail closed: with PUNCHLINE_ADMIN_TOKEN unset, every admin route 404s and the
// desk is simply not there.

// requireAdmin reports whether the request may use the admin API.
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.adminToken == "" {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	presented := strings.TrimSpace(r.Header.Get("Authorization"))
	if !constantTimeEqual(presented, "Bearer "+h.adminToken) {
		// Rate limit wrong guesses. The comparison is already constant time, so
		// this is about making an online brute force impractical rather than
		// about leaking timing. Only failures count against the limit, so a
		// working desk is never throttled.
		if !h.allowRequest(w, r, "admin_auth_failure", h.adminAuthFailureLimitPerMin) {
			return false
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "admin authorization required")
		return false
	}
	if !h.content.Available() {
		writeError(w, http.StatusServiceUnavailable, "the admin desk requires PostgreSQL")
		return false
	}
	return true
}

// cardReport is the player-facing report endpoint. It is deliberately public:
// reporting must work for a guest mid-game.
func (h *Handler) cardReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowRequest(w, r, "card_report", h.reportLimitPerMin) {
		return
	}
	var input content.ReportInput
	if err := decodeDailyBody(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid card report")
		return
	}
	result, err := h.content.Report(r.Context(), input, h.reporterSeed(r, input))
	if err != nil {
		h.metrics.recordContentAction("report", "error")
		h.writeContentError(w, err)
		return
	}
	h.metrics.recordContentAction("report", "ok")
	if result.Retired {
		h.metrics.recordCardAutoRetired()
	}
	writeJSON(w, http.StatusOK, result)
}

// reporterSeed decides who "one reporter" is. Keying on the client address
// counted networks, not people, and got both directions wrong: everyone at a
// party shares one WiFi, so a whole room agreeing a card is vile counted once
// and could never reach the auto-retire threshold, while one person with WiFi,
// cell data and a VPN counted three times and could retire any card alone.
//
// A player id verified against the room they are playing in is the honest unit.
// The address remains the fallback for anything that cannot prove a seat, and
// the per-address rate limit still applies either way.
func (h *Handler) reporterSeed(r *http.Request, input content.ReportInput) string {
	playerID := strings.TrimSpace(input.PlayerID)
	code := strings.ToUpper(strings.TrimSpace(input.RoomCode))
	if playerID == "" || input.Token == "" || code == "" {
		return h.proxyHeaders.clientIP(r)
	}
	room, err := h.manager.GetRoom(r.Context(), code)
	if err != nil || room == nil || !room.VerifyPlayer(playerID, input.Token) {
		return h.proxyHeaders.clientIP(r)
	}
	return "player:" + playerID
}

func (h *Handler) adminOverview(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	overview, err := h.content.Overview(r.Context())
	if err != nil {
		h.writeContentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (h *Handler) adminReports(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		reports, err := h.content.OpenReports(r.Context(), queryLimit(r))
		if err != nil {
			h.writeContentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
	case http.MethodPost:
		var input struct {
			CardKind   string `json:"card_kind"`
			CardID     string `json:"card_id"`
			Resolution string `json:"resolution"`
		}
		if err := decodeDailyBody(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid report resolution")
			return
		}
		if err := h.content.ResolveReports(r.Context(), input.CardKind, input.CardID, input.Resolution); err != nil {
			h.metrics.recordContentAction("resolve_report", "error")
			h.writeContentError(w, err)
			return
		}
		h.metrics.recordContentAction("resolve_report", "ok")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) adminCandidates(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		candidates, err := h.content.PendingCandidates(r.Context(), queryLimit(r))
		if err != nil {
			h.writeContentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates})
	case http.MethodPost:
		var input struct {
			CandidateID string `json:"candidate_id"`
			Decision    string `json:"decision"`
			Tier        string `json:"tier"`
		}
		if err := decodeDailyBody(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid candidate review")
			return
		}
		if err := h.content.ReviewCandidate(r.Context(), input.CandidateID, input.Decision, input.Tier); err != nil {
			h.metrics.recordContentAction("review_candidate", "error")
			h.writeContentError(w, err)
			return
		}
		h.metrics.recordContentAction("review_candidate", "ok")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) adminCards(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		query := r.URL.Query()
		kind := query.Get("kind")
		if kind == "" {
			kind = content.KindAnswer
		}
		cards, err := h.content.Cards(r.Context(), kind, query.Get("status"), query.Get("sort"), queryLimit(r))
		if err != nil {
			h.writeContentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cards": cards})
	case http.MethodPost:
		var input struct {
			CardKind string `json:"card_kind"`
			CardID   string `json:"card_id"`
			Status   string `json:"status"`
		}
		if err := decodeDailyBody(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid card update")
			return
		}
		if err := h.content.SetCardStatus(r.Context(), input.CardKind, input.CardID, input.Status); err != nil {
			h.metrics.recordContentAction("set_card_status", "error")
			h.writeContentError(w, err)
			return
		}
		h.metrics.recordContentAction("set_card_status", "ok")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// adminDailySubmission removes one player's daily answer. With no accounts to
// ban, removing the content is the moderation action available.
func (h *Handler) adminDailySubmission(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/daily-submissions/"), "/")
	if err := h.content.DeleteDailySubmission(r.Context(), id); err != nil {
		h.metrics.recordContentAction("delete_daily_submission", "error")
		h.writeContentError(w, err)
		return
	}
	h.metrics.recordContentAction("delete_daily_submission", "ok")
	w.WriteHeader(http.StatusNoContent)
}

func queryLimit(r *http.Request) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return value
}

func (h *Handler) writeContentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, content.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, content.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, content.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, content.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		log.Printf("content api: %v", err)
		writeError(w, http.StatusServiceUnavailable, "the content platform is temporarily unavailable")
	}
}
