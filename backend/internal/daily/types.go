package daily

import (
	"errors"
	"time"
)

const (
	StateOpen      = "OPEN_FOR_SUBMISSIONS"
	StateVoting    = "REVEALED_FOR_VOTING"
	StateFinalized = "FINALIZED"
)

var (
	ErrUnavailable   = errors.New("daily mode requires PostgreSQL")
	ErrNotFound      = errors.New("daily group or round not found")
	ErrUnauthorized  = errors.New("daily membership authorization required")
	ErrConflict      = errors.New("daily membership name is already in use")
	ErrGroupFull     = errors.New("daily group is full")
	ErrRoundClosed   = errors.New("daily submissions are closed")
	ErrVotingClosed  = errors.New("daily voting is not open")
	ErrOwnSubmission = errors.New("members cannot vote for their own submission")
	ErrInvalidInput  = errors.New("invalid daily mode input")
)

type CreateGroupInput struct {
	Name       string `json:"name"`
	PlayerName string `json:"player_name"`
	Timezone   string `json:"timezone"`
}

type JoinGroupInput struct {
	PlayerName string `json:"player_name"`
}

// UpdateGroupInput uses pointers so an omitted field means "leave unchanged"
// rather than "set to zero" — reveal_hour 0 is a legitimate midnight deadline.
type UpdateGroupInput struct {
	Name       *string `json:"name,omitempty"`
	Timezone   *string `json:"timezone,omitempty"`
	RevealHour *int    `json:"reveal_hour,omitempty"`
}

type Session struct {
	Group      Group      `json:"group"`
	Membership Membership `json:"membership"`
	Token      string     `json:"token"`
}

type Group struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Timezone    string `json:"timezone"`
	RevealHour  int    `json:"reveal_hour"`
	MemberCount int    `json:"member_count"`
}

type Membership struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type Submission struct {
	ID          string `json:"id"`
	AnswerText  string `json:"answer_text"`
	AuthorName  string `json:"author_name,omitempty"`
	VoteCount   int    `json:"vote_count,omitempty"`
	Mine        bool   `json:"mine,omitempty"`
	MyVote      bool   `json:"my_vote,omitempty"`
	Winner      bool   `json:"winner,omitempty"`
	SubmittedAt string `json:"submitted_at"`
}

type Round struct {
	ID                 string       `json:"id"`
	Number             int          `json:"number"`
	Date               string       `json:"date"`
	Prompt             string       `json:"prompt"`
	State              string       `json:"state"`
	OpensAt            time.Time    `json:"opens_at"`
	RevealAt           time.Time    `json:"reveal_at"`
	VotingClosesAt     time.Time    `json:"voting_closes_at"`
	SubmissionCount    int          `json:"submission_count"`
	MemberCount        int          `json:"member_count"`
	MySubmission       *Submission  `json:"my_submission,omitempty"`
	Submissions        []Submission `json:"submissions,omitempty"`
	MyVoteSubmissionID string       `json:"my_vote_submission_id,omitempty"`
}

type TodayView struct {
	Group      Group      `json:"group"`
	Membership Membership `json:"membership"`
	Today      Round      `json:"today"`
	Previous   *Round     `json:"previous,omitempty"`
	Streak     int        `json:"streak"`
	ServerTime time.Time  `json:"server_time"`
}

type TickResult struct {
	RoundsCreated   int `json:"rounds_created"`
	RoundsRevealed  int `json:"rounds_revealed"`
	RoundsFinalized int `json:"rounds_finalized"`
}
