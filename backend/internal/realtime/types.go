package realtime

import (
	"time"

	"punchline/backend/internal/cards"
)

type Phase string

const (
	PhaseLobby      Phase = "lobby"
	PhaseSubmitting Phase = "submitting"
	PhaseJudging    Phase = "judging"
	PhaseScoring    Phase = "scoring"
	PhaseFinished   Phase = "finished"
)

type Player struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	GuestToken string             `json:"-"`
	Score      int                `json:"score"`
	Connected  bool               `json:"connected"`
	IsJudge    bool               `json:"is_judge"`
	Submitted  bool               `json:"submitted"`
	Hand       []cards.AnswerCard `json:"hand,omitempty"`
}

type Submission struct {
	ID          string           `json:"id"`
	PlayerID    string           `json:"player_id"`
	PlayerName  string           `json:"player_name"`
	Answer      cards.AnswerCard `json:"answer"`
	SubmittedAt time.Time        `json:"submitted_at"`
	IsWinner    bool             `json:"is_winner"`
}

type RoomSnapshot struct {
	Code          string            `json:"code"`
	Phase         Phase             `json:"phase"`
	RoundNumber   int               `json:"round_number"`
	Prompt        *cards.PromptCard `json:"prompt,omitempty"`
	HostID        string            `json:"host_id,omitempty"`
	JudgeID       string            `json:"judge_id,omitempty"`
	ScoreLimit    int               `json:"score_limit"`
	RoundSeconds  int               `json:"round_seconds"`
	MaxPlayers    int               `json:"max_players"`
	ContentTier   string            `json:"content_tier"`
	PhaseDeadline *time.Time        `json:"phase_deadline,omitempty"`
	Players       []Player          `json:"players"`
	Submissions   []Submission      `json:"submissions,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// Settings are the host-configurable room options. A zero numeric field means
// "leave unchanged"; the engine clamps everything to safe ranges.
type Settings struct {
	ScoreLimit   int    `json:"score_limit,omitempty"`
	RoundSeconds int    `json:"round_seconds,omitempty"`
	MaxPlayers   int    `json:"max_players,omitempty"`
	ContentTier  string `json:"content_tier,omitempty"`
}

type ClientMessage struct {
	Type         string    `json:"type"`
	AnswerCardID string    `json:"answer_card_id,omitempty"`
	SubmissionID string    `json:"submission_id,omitempty"`
	Settings     *Settings `json:"settings,omitempty"`
}

type ServerMessage struct {
	Type  string       `json:"type"`
	Room  RoomSnapshot `json:"room"`
	Error string       `json:"error,omitempty"`
}
