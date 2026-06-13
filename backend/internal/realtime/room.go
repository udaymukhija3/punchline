package realtime

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"punchline/backend/internal/cards"
	"punchline/backend/internal/ws"
)

const (
	handSize      = 7
	maxNameLen    = 40
	judgeDuration = 45 * time.Second

	defaultScoreLimit = 5
	defaultSubmitSecs = 90
	defaultMaxPlayers = 12
	minPlayersToStart = 3
	minScoreLimit     = 1
	maxScoreLimit     = 20
	minSubmitSecs     = 15
	maxSubmitSecs     = 300
	maxPlayersCeiling = 20
)

var (
	ErrRoomFull           = errors.New("room is full")
	ErrRoomAlreadyStarted = errors.New("game already started")

	errNotHost      = errors.New("only the host can do that")
	errNotFound     = errors.New("player not found")
	errInvalidToken = errors.New("invalid player token")
)

type Room struct {
	mu            sync.Mutex
	code          string
	fullDeck      cards.Deck // unfiltered seed deck
	deck          cards.Deck // active deck filtered by contentTier
	phase         Phase
	phaseSeq      int
	phaseDeadline time.Time
	timer         *time.Timer
	roundNumber   int
	prompt        *cards.PromptCard
	hostID        string
	judgeID       string
	judgeIndex    int
	scoreLimit    int
	submitSecs    int
	maxPlayers    int
	contentTier   string
	players       map[string]*Player
	order         []string
	submissions   map[string]Submission
	answerPile    []cards.AnswerCard
	promptPile    []cards.PromptCard
	clients       map[string]map[*ws.Conn]bool
	createdAt     time.Time
	updatedAt     time.Time
}

func NewRoom(code string, deck cards.Deck) *Room {
	now := time.Now().UTC()
	r := &Room{
		code:        code,
		fullDeck:    deck,
		phase:       PhaseLobby,
		scoreLimit:  defaultScoreLimit,
		submitSecs:  defaultSubmitSecs,
		maxPlayers:  defaultMaxPlayers,
		contentTier: cards.TierParty,
		players:     map[string]*Player{},
		submissions: map[string]Submission{},
		clients:     map[string]map[*ws.Conn]bool{},
		createdAt:   now,
		updatedAt:   now,
	}
	r.deck = deck.For(r.contentTier)
	r.answerPile = r.deck.ShuffledAnswers()
	r.promptPile = r.deck.ShuffledPrompts()
	return r
}

func (r *Room) Join(name string) Player {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.joinLocked(name)
}

// TryJoin adds a player only while the room is joinable. This is the production
// path used by the HTTP API so capacity and phase checks happen under the same
// room lock as the roster mutation.
func (r *Room) TryJoin(name string) (Player, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.phase != PhaseLobby {
		return Player{}, ErrRoomAlreadyStarted
	}
	if len(r.order) >= r.maxPlayers {
		return Player{}, ErrRoomFull
	}
	return r.joinLocked(name), nil
}

func (r *Room) joinLocked(name string) Player {
	cleanName := clampName(strings.TrimSpace(name))
	if cleanName == "" {
		cleanName = "Guest"
	}
	p := Player{ID: newID("pl"), GuestToken: newSecret("gt"), Name: cleanName, Connected: false, Hand: r.drawAnswers(handSize)}
	r.players[p.ID] = &p
	r.order = append(r.order, p.ID)
	if r.hostID == "" {
		r.hostID = p.ID
	}
	r.touch()
	return p
}

func (r *Room) Attach(playerID string, guestToken string, conn *ws.Conn) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.players[playerID]
	if !ok {
		return errNotFound
	}
	if !validGuestToken(p.GuestToken, guestToken) {
		return errInvalidToken
	}
	p.Connected = true
	if r.hostID == "" {
		r.hostID = playerID
	}
	if r.clients[playerID] == nil {
		r.clients[playerID] = map[*ws.Conn]bool{}
	}
	r.clients[playerID][conn] = true
	r.touch()
	return nil
}

func (r *Room) Detach(playerID string, conn *ws.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if conns := r.clients[playerID]; conns != nil {
		delete(conns, conn)
		if len(conns) == 0 {
			delete(r.clients, playerID)
			if p := r.players[playerID]; p != nil {
				p.Connected = false
			}
			// Keep the game playable: if the host drops, hand the host
			// role to the first still-connected player.
			if playerID == r.hostID {
				r.reassignHostLocked()
			}
		}
	}
	r.touch()
}

func (r *Room) reassignHostLocked() {
	for _, id := range r.order {
		if p := r.players[id]; p != nil && p.Connected {
			r.hostID = id
			return
		}
	}
	r.hostID = ""
}

// StartGame begins the first round from the lobby. Host only.
func (r *Room) StartGame(playerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if playerID != r.hostID {
		return errNotHost
	}
	if r.phase != PhaseLobby {
		return errors.New("game already in progress")
	}
	if len(r.order) < minPlayersToStart {
		return errors.New("need at least 3 players to start")
	}
	r.judgeIndex = 0
	r.beginRoundLocked()
	return nil
}

func (r *Room) SubmitAnswer(playerID, answerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.phase != PhaseSubmitting {
		return errors.New("room is not accepting submissions")
	}
	if playerID == r.judgeID {
		return errors.New("the judge does not submit this round")
	}
	p := r.players[playerID]
	if p == nil {
		return errNotFound
	}
	for _, s := range r.submissions {
		if s.PlayerID == playerID {
			return errors.New("you already submitted")
		}
	}
	idx := -1
	var answer cards.AnswerCard
	for i, c := range p.Hand {
		if c.ID == answerID {
			idx = i
			answer = c
			break
		}
	}
	if idx < 0 {
		return errors.New("that card is not in your hand")
	}
	p.Hand = append(p.Hand[:idx], p.Hand[idx+1:]...)
	sub := Submission{ID: newID("sub"), PlayerID: playerID, PlayerName: p.Name, Answer: answer, SubmittedAt: time.Now().UTC()}
	r.submissions[sub.ID] = sub

	if r.allAnswerersSubmitted() {
		r.beginJudgingLocked()
	}
	r.touch()
	return nil
}

func (r *Room) PickWinner(judgeID, submissionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.phase != PhaseJudging {
		return errors.New("room is not in judging phase")
	}
	if judgeID != r.judgeID {
		return errors.New("only the judge can pick a winner")
	}
	if _, ok := r.submissions[submissionID]; !ok {
		return errors.New("submission not found")
	}
	r.awardLocked(submissionID)
	return nil
}

// NextRound advances from scoring to the next round. Host only.
func (r *Room) NextRound(playerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if playerID != r.hostID {
		return errNotHost
	}
	if r.phase != PhaseScoring {
		return errors.New("cannot advance before scoring")
	}
	r.judgeIndex = (r.judgeIndex + 1) % len(r.order)
	r.beginRoundLocked()
	return nil
}

// EndGame ends the match immediately. Host only.
func (r *Room) EndGame(playerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if playerID != r.hostID {
		return errNotHost
	}
	if r.phase == PhaseLobby {
		return errors.New("no game in progress")
	}
	r.setPhaseLocked(PhaseFinished, 0)
	r.touch()
	return nil
}

// SkipPrompt swaps the current prompt for a new one during the answer phase.
// Host only. Cards already played this round are returned to their owners'
// hands and the submit timer restarts, so nobody is penalised for a skip.
func (r *Room) SkipPrompt(playerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if playerID != r.hostID {
		return errNotHost
	}
	if r.phase != PhaseSubmitting {
		return errors.New("can only skip during the answer phase")
	}
	for _, s := range r.submissions {
		if p := r.players[s.PlayerID]; p != nil {
			p.Hand = append(p.Hand, s.Answer)
		}
	}
	r.submissions = map[string]Submission{}
	prompt := r.drawPrompt()
	r.prompt = &prompt
	r.setPhaseLocked(PhaseSubmitting, time.Duration(r.submitSecs)*time.Second)
	r.touch()
	return nil
}

// PlayAgain resets scores and returns the room to the lobby. Host only.
func (r *Room) PlayAgain(playerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if playerID != r.hostID {
		return errNotHost
	}
	if r.phase != PhaseFinished {
		return errors.New("game is not finished")
	}
	r.roundNumber = 0
	r.judgeIndex = 0
	r.judgeID = ""
	r.prompt = nil
	r.submissions = map[string]Submission{}
	for _, p := range r.players {
		p.Score = 0
		p.IsJudge = false
		p.Hand = r.refillHand(p.Hand)
	}
	r.setPhaseLocked(PhaseLobby, 0)
	r.touch()
	return nil
}

// IsFull reports whether the room is at its player capacity. This is a soft
// cap for joining; a rare simultaneous-join overshoot by one is harmless.
func (r *Room) IsFull() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order) >= r.maxPlayers
}

// UpdateSettings applies host-chosen room settings. Lobby only; values are
// clamped to safe ranges, and a content-tier change re-derives the deck and
// re-deals lobby hands so they match the new tier. A zero field means "keep".
func (r *Room) UpdateSettings(playerID string, s *Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if playerID != r.hostID {
		return errNotHost
	}
	if r.phase != PhaseLobby {
		return errors.New("settings can only change in the lobby")
	}
	if s == nil {
		return errors.New("no settings provided")
	}

	r.scoreLimit = clampInt(s.ScoreLimit, minScoreLimit, maxScoreLimit, r.scoreLimit)
	r.submitSecs = clampInt(s.RoundSeconds, minSubmitSecs, maxSubmitSecs, r.submitSecs)

	// Max players can never drop below the current roster (or the start floor).
	floor := len(r.order)
	if floor < minPlayersToStart {
		floor = minPlayersToStart
	}
	r.maxPlayers = clampInt(s.MaxPlayers, floor, maxPlayersCeiling, r.maxPlayers)

	if tier := normalizeTier(s.ContentTier); tier != "" && tier != r.contentTier {
		r.contentTier = tier
		r.deck = r.fullDeck.For(tier)
		r.answerPile = r.deck.ShuffledAnswers()
		r.promptPile = r.deck.ShuffledPrompts()
		for _, id := range r.order {
			r.players[id].Hand = r.drawAnswers(handSize)
		}
	}

	r.touch()
	return nil
}

func normalizeTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case cards.TierFamily:
		return cards.TierFamily
	case cards.TierParty:
		return cards.TierParty
	default:
		return "" // unrecognised / empty -> leave unchanged
	}
}

func clampInt(v, lo, hi, fallback int) int {
	if v == 0 {
		return fallback
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// --- internal transitions (caller holds the lock) ---

func (r *Room) beginRoundLocked() {
	r.roundNumber++
	r.submissions = map[string]Submission{}
	prompt := r.drawPrompt()
	r.prompt = &prompt
	r.judgeID = r.order[r.judgeIndex%len(r.order)]
	for _, id := range r.order {
		p := r.players[id]
		p.IsJudge = id == r.judgeID
		p.Hand = r.refillHand(p.Hand)
	}
	r.setPhaseLocked(PhaseSubmitting, time.Duration(r.submitSecs)*time.Second)
	r.touch()
}

func (r *Room) beginJudgingLocked() {
	if len(r.submissions) == 0 {
		// Nobody submitted — skip judging and let the host move on.
		r.setPhaseLocked(PhaseScoring, 0)
		r.touch()
		return
	}
	r.setPhaseLocked(PhaseJudging, judgeDuration)
	r.touch()
}

// awardLocked grants the point for a submission, then either finishes the
// game (score limit reached) or moves to the scoring/reveal phase.
func (r *Room) awardLocked(submissionID string) {
	sub := r.submissions[submissionID]
	sub.IsWinner = true
	r.submissions[submissionID] = sub
	reachedLimit := false
	if p := r.players[sub.PlayerID]; p != nil {
		p.Score++
		reachedLimit = p.Score >= r.scoreLimit
	}
	if reachedLimit {
		r.setPhaseLocked(PhaseFinished, 0)
	} else {
		r.setPhaseLocked(PhaseScoring, 0)
	}
	r.touch()
}

// allAnswerersSubmitted reports whether every non-judge player has submitted.
// It counts against the fixed roster rather than live connection state, so a
// player who submits and then disconnects cannot lower the threshold and force
// a premature advance to judging. Players who drop *without* submitting are
// handled by the submit-phase timer instead.
func (r *Room) allAnswerersSubmitted() bool {
	expected := len(r.order) - 1 // everyone except the judge
	return expected > 0 && len(r.submissions) >= expected
}

func (r *Room) refillHand(hand []cards.AnswerCard) []cards.AnswerCard {
	if len(hand) >= handSize {
		return hand
	}
	return append(hand, r.drawAnswers(handSize-len(hand))...)
}

// setPhaseLocked changes phase, bumps the sequence used to invalidate stale
// timers, and (re)arms the deadline timer for timed phases.
func (r *Room) setPhaseLocked(p Phase, d time.Duration) {
	r.phase = p
	r.phaseSeq++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if d > 0 {
		r.phaseDeadline = time.Now().UTC().Add(d)
		seq := r.phaseSeq
		r.timer = time.AfterFunc(d, func() { r.onDeadline(seq) })
	} else {
		r.phaseDeadline = time.Time{}
	}
}

// onDeadline fires when a timed phase expires. It only acts if the phase has
// not already advanced (guarded by phaseSeq).
func (r *Room) onDeadline(seq int) {
	r.mu.Lock()
	if r.phaseSeq != seq {
		r.mu.Unlock()
		return
	}
	switch r.phase {
	case PhaseSubmitting:
		r.beginJudgingLocked()
	case PhaseJudging:
		// Judge ran out of time — pick a random submission so play continues.
		r.awardLocked(r.randomSubmissionID())
	default:
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.Broadcast()
}

func (r *Room) randomSubmissionID() string {
	ids := make([]string, 0, len(r.submissions))
	for id := range r.submissions {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return ""
	}
	return ids[randInt(len(ids))]
}

// --- draw piles (caller holds the lock) ---

func (r *Room) drawAnswers(n int) []cards.AnswerCard {
	out := make([]cards.AnswerCard, 0, n)
	for i := 0; i < n; i++ {
		if len(r.answerPile) == 0 {
			r.answerPile = r.deck.ShuffledAnswers()
		}
		out = append(out, r.answerPile[len(r.answerPile)-1])
		r.answerPile = r.answerPile[:len(r.answerPile)-1]
	}
	return out
}

func (r *Room) drawPrompt() cards.PromptCard {
	if len(r.promptPile) == 0 {
		r.promptPile = r.deck.ShuffledPrompts()
	}
	p := r.promptPile[len(r.promptPile)-1]
	r.promptPile = r.promptPile[:len(r.promptPile)-1]
	return p
}

// --- snapshots / broadcast ---

func (r *Room) SnapshotFor(viewerID string) RoomSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(viewerID)
}

func (r *Room) Broadcast() {
	r.mu.Lock()
	recipients := make(map[*ws.Conn]RoomSnapshot)
	for pid, conns := range r.clients {
		snap := r.snapshotLocked(pid)
		for conn := range conns {
			recipients[conn] = snap
		}
	}
	r.mu.Unlock()

	for conn, snap := range recipients {
		_ = conn.WriteJSON(ServerMessage{Type: "room_state", Room: snap})
	}
}

func (r *Room) snapshotLocked(viewerID string) RoomSnapshot {
	submittedBy := make(map[string]bool, len(r.submissions))
	for _, s := range r.submissions {
		submittedBy[s.PlayerID] = true
	}

	players := make([]Player, 0, len(r.players))
	for _, id := range r.order {
		p := *r.players[id]
		p.Submitted = submittedBy[id]
		if id != viewerID {
			p.Hand = nil
		}
		players = append(players, p)
	}

	submissions := make([]Submission, 0, len(r.submissions))
	for _, s := range r.submissions {
		redacted := s
		if r.phase == PhaseSubmitting {
			// Hide content and authorship until the reveal.
			redacted.Answer = cards.AnswerCard{ID: s.ID, Text: "submitted"}
			redacted.PlayerID = ""
			redacted.PlayerName = ""
		} else if r.phase == PhaseJudging {
			// Reveal cards but keep authorship blind for fair judging.
			redacted.PlayerID = ""
			redacted.PlayerName = ""
		}
		submissions = append(submissions, redacted)
	}
	sort.Slice(submissions, func(i, j int) bool { return submissions[i].SubmittedAt.Before(submissions[j].SubmittedAt) })

	var deadline *time.Time
	if !r.phaseDeadline.IsZero() {
		d := r.phaseDeadline
		deadline = &d
	}

	return RoomSnapshot{
		Code:          r.code,
		Phase:         r.phase,
		RoundNumber:   r.roundNumber,
		Prompt:        r.prompt,
		HostID:        r.hostID,
		JudgeID:       r.judgeID,
		ScoreLimit:    r.scoreLimit,
		RoundSeconds:  r.submitSecs,
		MaxPlayers:    r.maxPlayers,
		ContentTier:   r.contentTier,
		PhaseDeadline: deadline,
		Players:       players,
		Submissions:   submissions,
		CreatedAt:     r.createdAt,
		UpdatedAt:     r.updatedAt,
	}
}

// Evictable reports whether the room can be reclaimed: no connected clients and
// no activity since the cutoff. (clients only holds entries for live sockets.)
func (r *Room) Evictable(cutoff time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.clients) == 0 && r.updatedAt.Before(cutoff)
}

// Shutdown stops the room's deadline timer. Called when the manager evicts it.
func (r *Room) Shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

func (r *Room) touch() { r.updatedAt = time.Now().UTC() }

func clampName(s string) string {
	if runes := []rune(s); len(runes) > maxNameLen {
		return string(runes[:maxNameLen])
	}
	return s
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func newSecret(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func validGuestToken(expected string, got string) bool {
	if expected == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}

func randInt(n int) int {
	if n <= 0 {
		return 0
	}
	x, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(x.Int64())
}
