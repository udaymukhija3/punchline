package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"punchline/backend/internal/daily"
)

func (h *Handler) StartDailyWorker(ctx context.Context, interval time.Duration) {
	h.daily.StartWorker(ctx, interval, func(result daily.TickResult, err error) {
		h.metrics.recordDailyWorker(result.RoundsCreated, result.RoundsRevealed, result.RoundsFinalized, err)
	})
}

const maxDailyBody = 8 << 10

func (h *Handler) dailyGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowRequest(w, r, "daily_group_create", h.dailyCreateLimitPerMin) {
		return
	}
	var input daily.CreateGroupInput
	if err := decodeDailyBody(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid daily group request")
		return
	}
	session, err := h.daily.CreateGroup(r.Context(), input)
	if err != nil {
		h.metrics.recordDailyAction("create_group", "error")
		h.writeDailyError(w, err)
		return
	}
	h.metrics.recordDailyAction("create_group", "ok")
	writeJSON(w, http.StatusCreated, session)
}

func (h *Handler) dailyGroupByCode(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/daily/groups/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodPatch {
		if !h.allowRequest(w, r, "daily_action", h.dailyActionLimitPerMin) {
			return
		}
		var input daily.UpdateGroupInput
		if err := decodeDailyBody(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid daily group update")
			return
		}
		group, err := h.daily.UpdateGroup(r.Context(), strings.ToUpper(parts[0]), bearerToken(r), input)
		if err != nil {
			h.metrics.recordDailyAction("update_group", "error")
			h.writeDailyError(w, err)
			return
		}
		h.metrics.recordDailyAction("update_group", "ok")
		writeJSON(w, http.StatusOK, group)
		return
	}
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodDelete {
		if !h.allowRequest(w, r, "daily_action", h.dailyActionLimitPerMin) {
			return
		}
		err := h.daily.DeleteGroup(r.Context(), strings.ToUpper(parts[0]), bearerToken(r))
		if err != nil {
			h.metrics.recordDailyAction("delete_group", "error")
			h.writeDailyError(w, err)
			return
		}
		h.metrics.recordDailyAction("delete_group", "ok")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	code, action := strings.ToUpper(parts[0]), parts[1]
	switch {
	case action == "join" && r.Method == http.MethodPost:
		if !h.allowRequest(w, r, "daily_group_join", h.dailyJoinLimitPerMin) {
			return
		}
		var input daily.JoinGroupInput
		if err := decodeDailyBody(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid daily join request")
			return
		}
		session, err := h.daily.JoinGroup(r.Context(), code, input)
		if err != nil {
			h.metrics.recordDailyAction("join_group", "error")
			h.writeDailyError(w, err)
			return
		}
		h.metrics.recordDailyAction("join_group", "ok")
		writeJSON(w, http.StatusCreated, session)
	case action == "today" && r.Method == http.MethodGet:
		if !h.allowRequest(w, r, "daily_read", h.dailyActionLimitPerMin) {
			return
		}
		view, err := h.daily.Today(r.Context(), code, bearerToken(r))
		if err != nil {
			h.metrics.recordDailyAction("today", "error")
			h.writeDailyError(w, err)
			return
		}
		h.metrics.recordDailyAction("today", "ok")
		writeJSON(w, http.StatusOK, view)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) dailyRoundAction(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/daily/rounds/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !h.allowRequest(w, r, "daily_action", h.dailyActionLimitPerMin) {
		return
	}
	roundID, action, token := parts[0], parts[1], bearerToken(r)
	var err error
	switch action {
	case "submit":
		var input struct {
			AnswerText string `json:"answer_text"`
		}
		if decodeErr := decodeDailyBody(w, r, &input); decodeErr != nil {
			writeError(w, http.StatusBadRequest, "invalid daily submission")
			return
		}
		err = h.daily.Submit(r.Context(), roundID, token, input.AnswerText)
	case "vote":
		var input struct {
			SubmissionID string `json:"submission_id"`
		}
		if decodeErr := decodeDailyBody(w, r, &input); decodeErr != nil {
			writeError(w, http.StatusBadRequest, "invalid daily vote")
			return
		}
		err = h.daily.Vote(r.Context(), roundID, token, input.SubmissionID)
	default:
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		h.metrics.recordDailyAction(action, "error")
		h.writeDailyError(w, err)
		return
	}
	h.metrics.recordDailyAction(action, "ok")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func decodeDailyBody(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDailyBody))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
}

func (h *Handler) writeDailyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, daily.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, daily.ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, daily.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, daily.ErrConflict), errors.Is(err, daily.ErrGroupFull),
		errors.Is(err, daily.ErrRoundClosed), errors.Is(err, daily.ErrVotingClosed),
		errors.Is(err, daily.ErrOwnSubmission):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, daily.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		log.Printf("daily api: %v", err)
		writeError(w, http.StatusServiceUnavailable, "daily mode is temporarily unavailable")
	}
}
