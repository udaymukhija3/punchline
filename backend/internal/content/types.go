package content

import (
	"errors"
	"time"
)

// Card kinds. Reports and moderation actions address one of the two card
// tables, so the kind travels with every id.
const (
	KindPrompt = "prompt"
	KindAnswer = "answer"
)

// PackSlugCommunity holds cards promoted from daily play.
const PackSlugCommunity = "community-daily"

var (
	ErrUnavailable  = errors.New("content platform requires PostgreSQL")
	ErrNotFound     = errors.New("card not found")
	ErrInvalidInput = errors.New("invalid content request")
	ErrConflict     = errors.New("that content has already been reviewed")
)

// ReportInput is what a player sends from a live room.
type ReportInput struct {
	CardKind string `json:"card_kind"`
	CardID   string `json:"card_id"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail"`
	RoomCode string `json:"room_code"`
	// PlayerID and Token identify the reporter as a person in a room. They are
	// verified against the room before they are trusted, and only ever reach
	// the database as part of a hash.
	PlayerID string `json:"player_id"`
	Token    string `json:"token"`
}

// ReportResult tells the client what happened without leaking moderation
// thresholds: retired is true only once the card is actually out of rotation.
type ReportResult struct {
	Recorded bool `json:"recorded"`
	Retired  bool `json:"retired"`
}

type Report struct {
	ID         string    `json:"id"`
	CardKind   string    `json:"card_kind"`
	CardID     string    `json:"card_id"`
	CardText   string    `json:"card_text"`
	CardStatus string    `json:"card_status"`
	Reason     string    `json:"reason"`
	Detail     string    `json:"detail"`
	RoomCode   string    `json:"room_code"`
	ReportsFor int       `json:"reports_for_card"`
	CreatedAt  time.Time `json:"created_at"`
}

type Candidate struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	AuthorName string    `json:"author_name"`
	VoteCount  int       `json:"vote_count"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// Card is the admin desk's view of a card, including how it performs in play.
type Card struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	ExternalID  string  `json:"external_id,omitempty"`
	Text        string  `json:"text"`
	Tier        string  `json:"tier"`
	Status      string  `json:"status"`
	Pack        string  `json:"pack"`
	TimesPlayed int     `json:"times_played"`
	WinCount    int     `json:"win_count"`
	SkipCount   int     `json:"skip_count"`
	ReportCount int     `json:"report_count"`
	WinRate     float64 `json:"win_rate"`
	SkipRate    float64 `json:"skip_rate"`
}

// Overview is the admin dashboard summary.
type Overview struct {
	OpenReports        int `json:"open_reports"`
	PendingCandidates  int `json:"pending_candidates"`
	ApprovedPrompts    int `json:"approved_prompts"`
	ApprovedAnswers    int `json:"approved_answers"`
	RetiredCards       int `json:"retired_cards"`
	CommunityPromoted  int `json:"community_promoted"`
	AutoRetireAt       int `json:"auto_retire_at"`
	DailyGroups        int `json:"daily_groups"`
	DailyRoundsToday   int `json:"daily_rounds_today"`
	ActiveDailyMembers int `json:"active_daily_members"`
}
