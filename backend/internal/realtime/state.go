package realtime

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"punchline/backend/internal/cards"
)

const currentRoomStateVersion = 1

var ErrRoomStateNotFound = errors.New("room state not found")

// RoomStateStore keeps the authoritative, recoverable state of an active
// room. The registry only maps a room code to an owner; this store lets a new
// process rebuild the game after that owner restarts or its lease expires.
type RoomStateStore interface {
	SaveRoomState(ctx context.Context, state PersistedRoomState) error
	LoadRoomState(ctx context.Context, code string) (PersistedRoomState, error)
	ResetRoomState(ctx context.Context, code string, instanceID string) error
	DeleteRoomState(ctx context.Context, code string, instanceID string) error
	StateStoreName() string
}

type PersistedRoomState struct {
	SchemaVersion int                `json:"schema_version"`
	Revision      uint64             `json:"revision"`
	InstanceID    string             `json:"-"`
	Code          string             `json:"code"`
	Phase         Phase              `json:"phase"`
	PhaseSeq      int                `json:"phase_seq"`
	PhaseDeadline time.Time          `json:"phase_deadline,omitempty"`
	RoundNumber   int                `json:"round_number"`
	Prompt        *cards.PromptCard  `json:"prompt,omitempty"`
	HostID        string             `json:"host_id,omitempty"`
	JudgeID       string             `json:"judge_id,omitempty"`
	JudgeIndex    int                `json:"judge_index"`
	ScoreLimit    int                `json:"score_limit"`
	SubmitSecs    int                `json:"submit_seconds"`
	MaxPlayers    int                `json:"max_players"`
	ContentTier   string             `json:"content_tier"`
	Players       []PersistedPlayer  `json:"players"`
	Order         []string           `json:"order"`
	Submissions   []Submission       `json:"submissions"`
	AnswerPile    []cards.AnswerCard `json:"answer_pile"`
	PromptPile    []cards.PromptCard `json:"prompt_pile"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

// PersistedPlayer intentionally includes the guest token. It is only written
// to the server-side state store and is never used in public room snapshots.
type PersistedPlayer struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	GuestToken string             `json:"guest_token"`
	Score      int                `json:"score"`
	IsComputer bool               `json:"is_computer"`
	IsJudge    bool               `json:"is_judge"`
	Hand       []cards.AnswerCard `json:"hand"`
}

// MemoryRoomStateStore keeps local development dependency-free. It is useful
// for tests, but intentionally does not survive a process restart.
type MemoryRoomStateStore struct {
	mu     sync.RWMutex
	states map[string]PersistedRoomState
}

func NewMemoryRoomStateStore() *MemoryRoomStateStore {
	return &MemoryRoomStateStore{states: map[string]PersistedRoomState{}}
}

func (s *MemoryRoomStateStore) SaveRoomState(ctx context.Context, state PersistedRoomState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state.Code = normalizeRoomCode(state.Code)
	if state.Code == "" {
		return errors.New("room state has no code")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.states[state.Code]; ok && existing.Revision > state.Revision {
		return nil
	}
	s.states[state.Code] = clonePersistedRoomState(state)
	return nil
}

func (s *MemoryRoomStateStore) LoadRoomState(ctx context.Context, code string) (PersistedRoomState, error) {
	if err := ctx.Err(); err != nil {
		return PersistedRoomState{}, err
	}
	s.mu.RLock()
	state, ok := s.states[normalizeRoomCode(code)]
	s.mu.RUnlock()
	if !ok {
		return PersistedRoomState{}, ErrRoomStateNotFound
	}
	return clonePersistedRoomState(state), nil
}

func (s *MemoryRoomStateStore) DeleteRoomState(ctx context.Context, code string, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.states, normalizeRoomCode(code))
	s.mu.Unlock()
	return nil
}

func (s *MemoryRoomStateStore) ResetRoomState(ctx context.Context, code string, _ string) error {
	return s.DeleteRoomState(ctx, code, "")
}

func (s *MemoryRoomStateStore) StateStoreName() string { return "memory" }

func clonePersistedRoomState(state PersistedRoomState) PersistedRoomState {
	cloned := state
	cloned.Prompt = clonePrompt(state.Prompt)
	cloned.Players = append([]PersistedPlayer(nil), state.Players...)
	for i := range cloned.Players {
		cloned.Players[i].Hand = append([]cards.AnswerCard(nil), state.Players[i].Hand...)
	}
	cloned.Order = append([]string(nil), state.Order...)
	cloned.Submissions = append([]Submission(nil), state.Submissions...)
	cloned.AnswerPile = append([]cards.AnswerCard(nil), state.AnswerPile...)
	cloned.PromptPile = append([]cards.PromptCard(nil), state.PromptPile...)
	return cloned
}

func clonePrompt(prompt *cards.PromptCard) *cards.PromptCard {
	if prompt == nil {
		return nil
	}
	cloned := *prompt
	return &cloned
}

func sortedSubmissions(values map[string]Submission) []Submission {
	submissions := make([]Submission, 0, len(values))
	for _, submission := range values {
		submissions = append(submissions, submission)
	}
	sort.Slice(submissions, func(i, j int) bool {
		if submissions[i].SubmittedAt.Equal(submissions[j].SubmittedAt) {
			return submissions[i].ID < submissions[j].ID
		}
		return submissions[i].SubmittedAt.Before(submissions[j].SubmittedAt)
	})
	return submissions
}
