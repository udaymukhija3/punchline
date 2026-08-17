package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"punchline/backend/internal/cards"
)

func newTestRoom() *Room {
	return NewRoom("TEST", cards.NewSeedDeck())
}

// completeReveal fast-forwards the staged reveal so tests about judging do not
// have to sleep through the choreography. It mirrors what the deadline timer
// does, including scheduling the computer turns that follow the transition.
func completeReveal(r *Room) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.phase == PhaseRevealing {
		r.advanceRevealLocked()
	}
	r.scheduleComputerTurnsLocked()
}

func TestJoinAssignsHostAndUniqueHand(t *testing.T) {
	r := newTestRoom()
	alice := r.Join("Alice")
	r.Join("Bob")

	if r.hostID != alice.ID {
		t.Fatalf("first joiner should be host, got %q", r.hostID)
	}
	if len(alice.Hand) != handSize {
		t.Fatalf("hand size = %d, want %d", len(alice.Hand), handSize)
	}
	seen := map[string]bool{}
	for _, c := range alice.Hand {
		if seen[c.ID] {
			t.Fatalf("duplicate card %q dealt into a single hand", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestFullRoomInitialDealDoesNotRecycleAnswers(t *testing.T) {
	r := newTestRoom()
	seen := map[string]string{}

	for i := 0; i < defaultMaxPlayers; i++ {
		player := r.Join(fmt.Sprintf("Player %d", i+1))
		if len(player.Hand) != handSize {
			t.Fatalf("hand size for %s = %d, want %d", player.Name, len(player.Hand), handSize)
		}
		for _, card := range player.Hand {
			if firstPlayer := seen[card.ID]; firstPlayer != "" {
				t.Fatalf("answer card %q was dealt to both %s and %s", card.ID, firstPlayer, player.Name)
			}
			seen[card.ID] = player.Name
		}
	}
}

func TestGuestTokenIsRequiredForAttachAndHiddenFromSnapshots(t *testing.T) {
	r := newTestRoom()
	alice := r.Join("Alice")

	if alice.GuestToken == "" {
		t.Fatal("join did not mint a guest token")
	}
	if err := r.Attach(alice.ID, "wrong-token", nil); !errors.Is(err, errInvalidToken) {
		t.Fatalf("err = %v, want errInvalidToken", err)
	}
	if err := r.Attach(alice.ID, alice.GuestToken, nil); err != nil {
		t.Fatalf("attach with valid token failed: %v", err)
	}
	if !r.players[alice.ID].Connected {
		t.Fatal("player was not marked connected after attach")
	}

	body, err := json.Marshal(r.SnapshotFor(alice.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), alice.GuestToken) {
		t.Fatal("snapshot leaked guest token")
	}
}

func TestHostOnlyStart(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	guest := r.Join("Bob")
	r.Join("Carol")

	if err := r.StartGame(guest.ID); err == nil {
		t.Fatal("non-host was allowed to start the game")
	}
	if err := r.StartGame(host.ID); err != nil {
		t.Fatalf("host start failed: %v", err)
	}
	if r.phase != PhaseSubmitting {
		t.Fatalf("phase = %q, want submitting", r.phase)
	}
}

func TestHostRoleRecoversAfterEmptyRoom(t *testing.T) {
	r := newTestRoom()
	alice := r.Join("Alice")

	if err := r.Attach(alice.ID, alice.GuestToken, nil); err != nil {
		t.Fatalf("attach host failed: %v", err)
	}
	r.Detach(alice.ID, nil)
	if r.hostID != "" {
		t.Fatalf("hostID = %q after empty room, want cleared", r.hostID)
	}

	bob := r.Join("Bob")
	if bob.ID != r.hostID {
		t.Fatalf("new joiner hostID = %q, want %q", r.hostID, bob.ID)
	}

	r.hostID = ""
	if err := r.Attach(alice.ID, alice.GuestToken, nil); err != nil {
		t.Fatalf("reattach failed: %v", err)
	}
	if r.hostID != alice.ID {
		t.Fatalf("reattached player hostID = %q, want %q", r.hostID, alice.ID)
	}
}

func TestNeedsThreePlayers(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	if err := r.StartGame(host.ID); err == nil {
		t.Fatal("game started with only 2 players")
	}
}

func TestStartComputerGameAddsComputerPlayersAndUsesRealLoop(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")

	if err := r.StartComputerGame(host.ID); err != nil {
		t.Fatalf("start computer game failed: %v", err)
	}
	snap := r.SnapshotFor(host.ID)
	if snap.Phase != PhaseSubmitting {
		t.Fatalf("phase = %q, want submitting", snap.Phase)
	}
	if len(snap.Players) != minPlayersToStart {
		t.Fatalf("players = %d, want %d", len(snap.Players), minPlayersToStart)
	}
	computers := 0
	var computerAnswererID string
	for _, p := range snap.Players {
		if p.IsComputer {
			computers++
			if p.ID != snap.JudgeID {
				computerAnswererID = p.ID
			}
		}
	}
	if computers != 2 {
		t.Fatalf("computer players = %d, want 2", computers)
	}
	if !r.players[snap.JudgeID].IsComputer {
		t.Fatal("first computer game judge should be a computer")
	}

	hostHand := r.SnapshotFor(host.ID).Players[0].Hand
	if err := r.SubmitAnswer(host.ID, hostHand[0].ID); err != nil {
		t.Fatalf("host submit failed: %v", err)
	}
	r.submitComputerAnswer(computerAnswererID, r.phaseSeq)
	if r.phase != PhaseRevealing {
		t.Fatalf("phase after computer submit = %q, want revealing", r.phase)
	}
	completeReveal(r)
	if r.phase != PhaseJudging {
		t.Fatalf("phase after reveal = %q, want judging", r.phase)
	}
	r.pickComputerWinner(r.judgeID, r.phaseSeq)
	if r.phase != PhaseScoring {
		t.Fatalf("phase after computer judge = %q, want scoring", r.phase)
	}
}

func TestTryJoinRejectsFullRoomAtAnyPhase(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	r.Join("Carol")

	if err := r.UpdateSettings(host.ID, &Settings{MaxPlayers: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TryJoin("Dave"); !errors.Is(err, ErrRoomFull) {
		t.Fatalf("err = %v, want ErrRoomFull", err)
	}

	// A started game no longer refuses arrivals -- see
	// TestLateJoinerPlaysFromTheNextRound -- but a full one still does, at any
	// phase.
	r.maxPlayers = 12
	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TryJoin("Eve"); err != nil {
		t.Fatalf("late join = %v, want success", err)
	}
	r.mu.Lock()
	r.maxPlayers = len(r.order)
	r.mu.Unlock()
	if _, err := r.TryJoin("Frank"); !errors.Is(err, ErrRoomFull) {
		t.Fatalf("join into a full started room = %v, want ErrRoomFull", err)
	}
}

// drives one full round: deal -> both non-judges submit -> judge picks.
func playRound(t *testing.T, r *Room, ids []string) {
	t.Helper()
	judge := r.judgeID
	for _, id := range ids {
		if id == judge {
			continue
		}
		card := r.players[id].Hand[0]
		if err := r.SubmitAnswer(id, card.ID); err != nil {
			t.Fatalf("submit failed for %s: %v", id, err)
		}
	}
	completeReveal(r)
	if r.phase != PhaseJudging {
		t.Fatalf("after all submissions phase = %q, want judging", r.phase)
	}
	var subID string
	for id := range r.submissions {
		subID = id
		break
	}
	if err := r.PickWinner(judge, subID); err != nil {
		t.Fatalf("pick winner failed: %v", err)
	}
}

func TestFullRoundScoresAndRotatesJudge(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	r.Join("Carol")
	ids := append([]string{}, r.order...)

	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	firstJudge := r.judgeID
	playRound(t, r, ids)

	if r.phase != PhaseScoring {
		t.Fatalf("phase = %q, want scoring", r.phase)
	}
	total := 0
	for _, p := range r.players {
		total += p.Score
	}
	if total != 1 {
		t.Fatalf("exactly one point should be awarded, got %d", total)
	}
	if err := r.NextRound(host.ID); err != nil {
		t.Fatal(err)
	}
	if r.judgeID == firstJudge {
		t.Fatal("judge did not rotate to a new player")
	}
}

func TestRoundWaitsForEveryAnswerer(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	r.Join("Carol")
	r.Join("Dave")
	ids := append([]string{}, r.order...)

	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	judge := r.judgeID

	submitted := 0
	for _, id := range ids {
		if id == judge {
			continue
		}
		card := r.players[id].Hand[0]
		if err := r.SubmitAnswer(id, card.ID); err != nil {
			t.Fatalf("submit failed: %v", err)
		}
		submitted++
		if submitted < 3 && r.phase != PhaseSubmitting {
			t.Fatalf("advanced after %d/3 submissions; a dropped submitter must not lower the bar", submitted)
		}
	}
	if r.phase != PhaseRevealing {
		t.Fatalf("phase = %q after all answerers submitted, want revealing", r.phase)
	}
}

func TestWinConditionFinishesGame(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	r.Join("Carol")
	r.scoreLimit = 1 // in-package test: finish after a single point
	ids := append([]string{}, r.order...)

	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	playRound(t, r, ids)
	if r.phase != PhaseFinished {
		t.Fatalf("phase = %q, want finished", r.phase)
	}

	if err := r.PlayAgain(host.ID); err != nil {
		t.Fatalf("play again failed: %v", err)
	}
	if r.phase != PhaseLobby {
		t.Fatalf("after play_again phase = %q, want lobby", r.phase)
	}
	for _, p := range r.players {
		if p.Score != 0 {
			t.Fatalf("scores not reset: %s has %d", p.Name, p.Score)
		}
	}
}

func TestSubmittingPhaseRedactsAnswers(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	bob := r.Join("Bob")
	r.Join("Carol")
	ids := append([]string{}, r.order...)
	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	// one non-judge submits, room stays in submitting (needs 2)
	var submitter string
	for _, id := range ids {
		if id != r.judgeID {
			submitter = id
			break
		}
	}
	card := r.players[submitter].Hand[0]
	if err := r.SubmitAnswer(submitter, card.ID); err != nil {
		t.Fatal(err)
	}
	snap := r.SnapshotFor(bob.ID)
	if snap.Phase != PhaseSubmitting {
		t.Fatalf("expected still submitting, got %q", snap.Phase)
	}
	for _, s := range snap.Submissions {
		if s.Answer.Text != "submitted" || s.PlayerName != "" {
			t.Fatalf("submission leaked before reveal: %+v", s)
		}
	}
}

func TestJudgingSnapshotRevealsCardsButHidesAuthorsAndOtherHands(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	r.Join("Carol")
	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}

	answererIDs := make([]string, 0, 2)
	submitted := map[string]string{}
	for _, id := range r.order {
		if id == r.judgeID {
			continue
		}
		answererIDs = append(answererIDs, id)
		card := r.players[id].Hand[0]
		submitted[card.ID] = card.Text
		if err := r.SubmitAnswer(id, card.ID); err != nil {
			t.Fatalf("submit failed: %v", err)
		}
	}
	completeReveal(r)
	if r.phase != PhaseJudging {
		t.Fatalf("phase = %q, want judging", r.phase)
	}

	judgeSnap := r.SnapshotFor(r.judgeID)
	if len(judgeSnap.Submissions) != len(answererIDs) {
		t.Fatalf("judge saw %d submissions, want %d", len(judgeSnap.Submissions), len(answererIDs))
	}
	for _, s := range judgeSnap.Submissions {
		if s.PlayerID != "" || s.PlayerName != "" {
			t.Fatalf("judging snapshot leaked authorship: %+v", s)
		}
		if s.Answer.Text == "" || s.Answer.Text == "submitted" {
			t.Fatalf("judging snapshot did not reveal answer text: %+v", s)
		}
		if submitted[s.Answer.ID] != s.Answer.Text {
			t.Fatalf("judging snapshot showed unexpected answer card: %+v", s.Answer)
		}
	}
	for _, p := range judgeSnap.Players {
		if p.ID == r.judgeID {
			if len(p.Hand) == 0 {
				t.Fatal("viewer should still receive their own hand")
			}
			continue
		}
		if len(p.Hand) != 0 {
			t.Fatalf("judge snapshot leaked %s's hand", p.Name)
		}
	}

	answererSnap := r.SnapshotFor(answererIDs[0])
	for _, p := range answererSnap.Players {
		if p.ID == answererIDs[0] {
			if len(p.Hand) == 0 {
				t.Fatal("answerer should receive their own remaining hand")
			}
			continue
		}
		if len(p.Hand) != 0 {
			t.Fatalf("answerer snapshot leaked %s's hand", p.Name)
		}
	}
}

func TestJudgeOnlyPickWinnerAndHostOnlyRoundAdvance(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	r.Join("Carol")
	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}

	var nonJudgeID string
	for _, id := range r.order {
		if id == r.judgeID {
			continue
		}
		if nonJudgeID == "" {
			nonJudgeID = id
		}
		card := r.players[id].Hand[0]
		if err := r.SubmitAnswer(id, card.ID); err != nil {
			t.Fatalf("submit failed: %v", err)
		}
	}
	completeReveal(r)
	if r.phase != PhaseJudging {
		t.Fatalf("phase = %q, want judging", r.phase)
	}
	var subID string
	for id := range r.submissions {
		subID = id
		break
	}

	if err := r.PickWinner(nonJudgeID, subID); err == nil {
		t.Fatal("non-judge was allowed to pick the winner")
	}
	if err := r.PickWinner(r.judgeID, subID); err != nil {
		t.Fatalf("judge pick failed: %v", err)
	}
	if err := r.NextRound(nonJudgeID); err == nil {
		t.Fatal("non-host was allowed to advance the round")
	}
	if err := r.NextRound(host.ID); err != nil {
		t.Fatalf("host next round failed: %v", err)
	}
}

// A NUL byte in a player name used to reach the roster, where it made the
// room's state unserializable to Postgres JSONB. Every later join and every
// later action on that room then failed, unrecoverably, for everyone in it.
func TestJoinRejectsUnpersistableNames(t *testing.T) {
	rejected := []struct {
		label string
		name  string
	}{
		{"nul byte", "Attacker\x00x"},
		{"nul only", "\x00"},
		{"bell", "Bo\ab"},
		{"newline", "line1\nline2"},
		{"tab", "col1\tcol2"},
		{"carriage return", "Bob\r"},
		{"escape", "Bob\x1b[31m"},
		{"delete", "Bob\x7f"},
	}
	for _, tc := range rejected {
		room := newTestRoom()
		if _, err := room.TryJoin(tc.name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("%s: TryJoin = %v, want ErrInvalidName", tc.label, err)
		}
		if got := len(room.SnapshotFor("").Players); got != 0 {
			t.Fatalf("%s: rejected join left %d players in the roster", tc.label, got)
		}
		// The room has to stay usable for the next player through the door.
		if _, err := room.TryJoin("Innocent"); err != nil {
			t.Fatalf("%s: legitimate join after rejection = %v", tc.label, err)
		}
	}

	accepted := []struct {
		label string
		name  string
	}{
		{"emoji", "🎉 Sam"},
		{"zwj family", "👨‍👩‍👧‍👦"},
		{"accents", "Zoë Ünicode"},
		{"cjk", "田中さん"},
		{"rtl override", "Bob‮evil"},
		{"zero width", "A​B"},
		{"punctuation", "D'Angelo-Smith (Jr.)"},
	}
	for _, tc := range accepted {
		room := newTestRoom()
		player, err := room.TryJoin(tc.name)
		if err != nil {
			t.Fatalf("%s: TryJoin = %v, want success", tc.label, err)
		}
		if player.Name != tc.name {
			t.Fatalf("%s: stored name = %q, want %q", tc.label, player.Name, tc.name)
		}
	}
}

// Names that are only whitespace, or longer than the roster carries, keep
// their existing behaviour: normalise, do not reject.
func TestJoinStillNormalisesEdgeNames(t *testing.T) {
	room := newTestRoom()
	blank, err := room.TryJoin("     ")
	if err != nil || blank.Name != "Guest" {
		t.Fatalf("whitespace name = (%q, %v), want (\"Guest\", nil)", blank.Name, err)
	}
	long, err := room.TryJoin(strings.Repeat("é", 80))
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(long.Name)); got != maxNameLen {
		t.Fatalf("long name kept %d runes, want %d", got, maxNameLen)
	}
}

// A join that fails after the roster mutation must not hold a seat. The player
// never receives its guest token, so nothing can ever occupy that seat again.
func TestUndoJoinFreesTheSeat(t *testing.T) {
	room := newTestRoom()
	keep, err := room.TryJoin("Keeper")
	if err != nil {
		t.Fatal(err)
	}
	ghost, err := room.TryJoin("Ghost")
	if err != nil {
		t.Fatal(err)
	}
	room.UndoJoin(ghost.ID)

	snap := room.SnapshotFor("")
	if len(snap.Players) != 1 || snap.Players[0].Name != "Keeper" {
		t.Fatalf("roster after undo = %+v, want only Keeper", snap.Players)
	}
	if room.SnapshotFor(keep.ID).HostID != keep.ID {
		t.Fatal("undoing a later join disturbed the host")
	}
	// Undoing an unknown or already-removed player is a no-op, not a panic.
	room.UndoJoin(ghost.ID)
	room.UndoJoin("pl_missing")
}

// Arriving late used to be impossible: a friend who clicked the invite link
// after the host started got "game already started" and was locked out until
// the game ended, which is the most common thing that happens at a party.
func TestLateJoinerPlaysFromTheNextRound(t *testing.T) {
	room := newTestRoom()
	host := room.Join("Host")
	room.Join("Two")
	room.Join("Three")
	if err := room.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}

	late, err := room.TryJoin("Latecomer")
	if err != nil {
		t.Fatalf("late join = %v, want success", err)
	}
	if len(late.Hand) != handSize {
		t.Fatalf("late joiner got %d cards, want %d", len(late.Hand), handSize)
	}
	if late.IsJudge {
		t.Fatal("a late joiner must not take over judging a round already underway")
	}
	snap := room.SnapshotFor(late.ID)
	if len(snap.Players) != 4 {
		t.Fatalf("roster = %d, want 4", len(snap.Players))
	}
	if snap.Phase != PhaseSubmitting {
		t.Fatalf("phase = %q, want the round to carry on", snap.Phase)
	}
	// They can play the round they walked into.
	if err := room.SubmitAnswer(late.ID, late.Hand[0].ID); err != nil {
		t.Fatalf("late joiner could not answer: %v", err)
	}
	// And a full room still refuses, whatever the phase.
	room.mu.Lock()
	room.maxPlayers = 4
	room.mu.Unlock()
	if _, err := room.TryJoin("TooLate"); !errors.Is(err, ErrRoomFull) {
		t.Fatalf("join into a full room = %v, want ErrRoomFull", err)
	}
}

// Leaving used to only drop the socket, so the seat was occupied forever.
func TestLeaveFreesTheSeatInTheLobby(t *testing.T) {
	room := newTestRoom()
	host := room.Join("Host")
	quitter := room.Join("Quitter")
	room.Join("Three")

	if err := room.Leave(quitter.ID); err != nil {
		t.Fatal(err)
	}
	snap := room.SnapshotFor(host.ID)
	if len(snap.Players) != 2 {
		t.Fatalf("roster = %d, want 2 after a leave", len(snap.Players))
	}
	for _, p := range snap.Players {
		if p.Name == "Quitter" {
			t.Fatal("the player who left is still holding a seat")
		}
	}
	if snap.HostID != host.ID {
		t.Fatal("an unrelated leave moved the host")
	}
	if err := room.Leave(quitter.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("second leave = %v, want errNotFound", err)
	}
}

// The host leaving hands the room to someone still there.
func TestLeaveMovesTheHost(t *testing.T) {
	room := newTestRoom()
	host := room.Join("Host")
	next := room.Join("Next")
	room.Join("Third")
	if err := room.Leave(host.ID); err != nil {
		t.Fatal(err)
	}
	if got := room.SnapshotFor(next.ID).HostID; got != next.ID {
		t.Fatalf("host after the host left = %q, want %q", got, next.ID)
	}
}

// A round nobody can award is void rather than stuck. The played cards go back
// to their owners instead of being burned on a round that never scored.
func TestJudgeLeavingVoidsTheRound(t *testing.T) {
	room := newTestRoom()
	host := room.Join("Host")
	two := room.Join("Two")
	three := room.Join("Three")
	four := room.Join("Four")
	if err := room.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	judgeID := room.SnapshotFor(host.ID).JudgeID
	answerers := []Player{}
	for _, p := range []Player{host, two, three, four} {
		if p.ID != judgeID {
			answerers = append(answerers, p)
		}
	}
	played := answerers[0]
	before := len(room.SnapshotFor(played.ID).Players)
	if err := room.SubmitAnswer(played.ID, played.Hand[0].ID); err != nil {
		t.Fatal(err)
	}

	if err := room.Leave(judgeID); err != nil {
		t.Fatal(err)
	}
	snap := room.SnapshotFor(played.ID)
	if snap.Phase != PhaseScoring {
		t.Fatalf("phase after the judge left = %q, want scoring so the host can move on", snap.Phase)
	}
	if len(snap.Submissions) != 0 {
		t.Fatalf("void round kept %d submissions", len(snap.Submissions))
	}
	if len(snap.Players) != before-1 {
		t.Fatalf("roster = %d, want %d", len(snap.Players), before-1)
	}
	var mine *Player
	for i := range snap.Players {
		if snap.Players[i].ID == played.ID {
			mine = &snap.Players[i]
		}
	}
	if mine == nil || len(mine.Hand) != handSize {
		t.Fatalf("played card was not returned; hand = %v", mine)
	}
	// Play continues: the next round deals a fresh judge from the survivors.
	if err := room.NextRound(room.SnapshotFor("").HostID); err != nil {
		t.Fatalf("next round after a void round = %v", err)
	}
	if room.SnapshotFor("").JudgeID == "" {
		t.Fatal("next round has no judge")
	}
}

// The room may have been waiting only on the player who walked out.
func TestLeavingCompletesAWaitingRound(t *testing.T) {
	room := newTestRoom()
	host := room.Join("Host")
	two := room.Join("Two")
	three := room.Join("Three")
	if err := room.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	judgeID := room.SnapshotFor("").JudgeID
	var waiting, submitted Player
	for _, p := range []Player{host, two, three} {
		if p.ID == judgeID {
			continue
		}
		if submitted.ID == "" {
			submitted = p
		} else {
			waiting = p
		}
	}
	if err := room.SubmitAnswer(submitted.ID, submitted.Hand[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := room.SnapshotFor("").Phase; got != PhaseSubmitting {
		t.Fatalf("phase = %q, want to still be waiting", got)
	}
	// Two players left is below the floor, so this also ends the game -- which
	// is the honest outcome, not a room stuck waiting for someone who left.
	if err := room.Leave(waiting.ID); err != nil {
		t.Fatal(err)
	}
	if got := room.SnapshotFor("").Phase; got != PhaseFinished {
		t.Fatalf("phase = %q, want finished once the room drops below %d players", got, minPlayersToStart)
	}
}

// Dropping below the floor mid-game ends it rather than leaving a room that
// cannot deal a round.
func TestLeavingBelowThePlayerFloorEndsTheGame(t *testing.T) {
	room := newTestRoom()
	host := room.Join("Host")
	two := room.Join("Two")
	room.Join("Three")
	if err := room.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	if err := room.Leave(two.ID); err != nil {
		t.Fatal(err)
	}
	if got := room.SnapshotFor(host.ID).Phase; got != PhaseFinished {
		t.Fatalf("phase = %q, want finished", got)
	}
	// The host can still rebuild the table from the game-over screen.
	if err := room.PlayAgain(room.SnapshotFor("").HostID); err != nil {
		t.Fatalf("play again after a walkout = %v", err)
	}
}

// Judge rotation is an index into the roster, so removing players must not make
// it skip, repeat, or run off the end.
func TestJudgeRotationSurvivesDepartures(t *testing.T) {
	room := newTestRoom()
	host := room.Join("Host")
	players := []Player{host, room.Join("B"), room.Join("C"), room.Join("D"), room.Join("E")}
	if err := room.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	// Drop someone who sits before the judge, then someone after.
	if err := room.Leave(players[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := room.Leave(players[4].ID); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 6; round++ {
		snap := room.SnapshotFor("")
		if snap.Phase == PhaseFinished {
			break
		}
		if snap.Phase == PhaseSubmitting || snap.Phase == PhaseRevealing || snap.Phase == PhaseJudging {
			judge := snap.JudgeID
			if judge != "" && room.SnapshotFor(judge).JudgeID != judge {
				t.Fatal("judge disagrees with itself between views")
			}
			found := false
			for _, p := range snap.Players {
				if p.ID == judge {
					found = true
				}
			}
			if judge != "" && !found {
				t.Fatalf("round %d judge %q is not in the roster", round, judge)
			}
		}
		room.mu.Lock()
		room.phase = PhaseScoring
		room.mu.Unlock()
		if err := room.NextRound(room.SnapshotFor("").HostID); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

// startRoundAndSubmitAll starts a game and has every answerer play a card,
// returning the room, the judge, and the real text of each submitted card.
func startRoundAndSubmitAll(t *testing.T, names ...string) (*Room, string, map[string]string) {
	t.Helper()
	r := newTestRoom()
	host := r.Join(names[0])
	for _, name := range names[1:] {
		r.Join(name)
	}
	if err := r.StartGame(host.ID); err != nil {
		t.Fatalf("start game: %v", err)
	}
	textByCard := map[string]string{}
	for _, id := range r.order {
		if id == r.judgeID {
			continue
		}
		card := r.players[id].Hand[0]
		textByCard[card.ID] = card.Text
		if err := r.SubmitAnswer(id, card.ID); err != nil {
			t.Fatalf("submit failed for %s: %v", id, err)
		}
	}
	return r, r.judgeID, textByCard
}

func TestRevealTurnsCardsOverOneAtATime(t *testing.T) {
	r, judge, textByCard := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")

	if r.phase != PhaseRevealing {
		t.Fatalf("phase after every answer = %q, want revealing", r.phase)
	}
	answerers := len(r.order) - 1

	for step := 1; step <= answerers; step++ {
		snap := r.SnapshotFor(judge)
		if snap.RevealIndex != step {
			t.Fatalf("step %d: reveal_index = %d, want %d", step, snap.RevealIndex, step)
		}
		faceUp := 0
		for i, s := range snap.Submissions {
			if !s.Revealed {
				if s.Answer.Text != "submitted" {
					t.Fatalf("step %d: face-down card %d leaked its text %q", step, i, s.Answer.Text)
				}
				continue
			}
			faceUp++
			// A face-up card must carry its real text, and must come before
			// every face-down one so the table fills in visual order.
			if want := textByCard[s.Answer.ID]; want == "" || s.Answer.Text != want {
				t.Fatalf("step %d: revealed card %d text = %q, want %q", step, i, s.Answer.Text, want)
			}
			if i >= step {
				t.Fatalf("step %d: card at position %d is face up out of order", step, i)
			}
		}
		if faceUp != step {
			t.Fatalf("step %d: %d cards face up, want %d", step, faceUp, step)
		}
		if step < answerers {
			r.mu.Lock()
			r.advanceRevealLocked()
			r.mu.Unlock()
		}
	}

	// The last card gets its own beat before the judge's controls appear.
	if r.phase != PhaseRevealing {
		t.Fatalf("phase with the final card just turned = %q, want revealing", r.phase)
	}
	r.mu.Lock()
	r.advanceRevealLocked()
	r.mu.Unlock()
	if r.phase != PhaseJudging {
		t.Fatalf("phase after the last beat = %q, want judging", r.phase)
	}
}

func TestRevealKeepsAuthorshipBlindThroughout(t *testing.T) {
	r, judge, _ := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")

	for r.phase == PhaseRevealing {
		for _, viewer := range append([]string{judge, ""}, r.order...) {
			for _, s := range r.SnapshotFor(viewer).Submissions {
				if s.PlayerID != "" || s.PlayerName != "" {
					t.Fatalf("reveal snapshot for %q leaked authorship: %+v", viewer, s)
				}
			}
		}
		r.mu.Lock()
		r.advanceRevealLocked()
		r.mu.Unlock()
	}
}

func TestJudgeCannotPickBeforeTheRevealFinishes(t *testing.T) {
	r, judge, _ := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")

	var subID string
	for id := range r.submissions {
		subID = id
		break
	}
	if err := r.PickWinner(judge, subID); err == nil {
		t.Fatal("judge picked a winner while cards were still turning over")
	}

	completeReveal(r)
	if err := r.PickWinner(judge, subID); err != nil {
		t.Fatalf("judge pick after the reveal failed: %v", err)
	}
	if r.phase != PhaseScoring {
		t.Fatalf("phase after the pick = %q, want scoring", r.phase)
	}
}

// The table used to be laid out by submit time, so the first card revealed was
// always the fastest answerer's -- authorship leaking through position.
func TestRevealOrderDoesNotTrackSubmitOrder(t *testing.T) {
	firstSeen := map[string]int{}
	const trials = 60
	for i := 0; i < trials; i++ {
		r, judge, _ := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")
		snap := r.SnapshotFor(judge)
		for _, s := range snap.Submissions {
			if s.Revealed {
				firstSeen[r.submissions[s.ID].PlayerName]++
				break
			}
		}
	}
	if len(firstSeen) < 2 {
		t.Fatalf("the same author was revealed first in all %d rounds: %v", trials, firstSeen)
	}
}

func TestRevealCompressesForABigTable(t *testing.T) {
	if got := revealStepFor(3); got != revealStepInterval {
		t.Fatalf("small table step = %v, want %v", got, revealStepInterval)
	}
	big := revealStepFor(11)
	if big >= revealStepInterval {
		t.Fatalf("a full table should quicken the beat, got %v", big)
	}
	if total := 11 * big; total > revealTotalBudget {
		t.Fatalf("full-table reveal runs %v, over the %v budget", total, revealTotalBudget)
	}
	if floor := revealStepFor(1000); floor < revealStepMin {
		t.Fatalf("step %v fell below the %v floor", floor, revealStepMin)
	}
}

func TestLeavingMidRevealDropsOnlyThatCard(t *testing.T) {
	r, judge, _ := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")

	var leaver string
	for _, id := range r.order {
		if id != judge {
			leaver = id
			break
		}
	}
	if err := r.Leave(leaver); err != nil {
		t.Fatalf("leave: %v", err)
	}

	r.mu.Lock()
	order := append([]string(nil), r.revealOrder...)
	index := r.revealIndex
	r.mu.Unlock()

	if len(order) != len(r.submissions) {
		t.Fatalf("reveal order has %d slots for %d submissions", len(order), len(r.submissions))
	}
	for _, id := range order {
		if _, ok := r.submissions[id]; !ok {
			t.Fatalf("reveal order kept a slot for the departed submission %q", id)
		}
	}
	if index > len(order) {
		t.Fatalf("reveal index %d exceeds the %d remaining cards", index, len(order))
	}
	if snap := r.SnapshotFor(judge); len(snap.Submissions) != len(order) {
		t.Fatalf("snapshot showed %d cards, want %d", len(snap.Submissions), len(order))
	}

	completeReveal(r)
	if r.phase != PhaseJudging {
		t.Fatalf("phase after a mid-reveal departure = %q, want judging", r.phase)
	}
}

func TestRevealSurvivesRestore(t *testing.T) {
	r, judge, _ := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")
	r.mu.Lock()
	r.advanceRevealLocked()
	wantOrder := append([]string(nil), r.revealOrder...)
	wantIndex := r.revealIndex
	r.mu.Unlock()

	restored, err := RestoreRoom(r.PersistedState(), cards.NewSeedDeck())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.phase != PhaseRevealing {
		t.Fatalf("restored phase = %q, want revealing", restored.phase)
	}
	if restored.revealIndex != wantIndex {
		t.Fatalf("restored reveal index = %d, want %d", restored.revealIndex, wantIndex)
	}
	if strings.Join(restored.revealOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("restored reveal order = %v, want %v", restored.revealOrder, wantOrder)
	}
	// The same cards must still be face up, and no more.
	faceUp := 0
	for _, s := range restored.SnapshotFor(judge).Submissions {
		if s.Revealed {
			faceUp++
		}
	}
	if faceUp != wantIndex {
		t.Fatalf("restored snapshot has %d cards face up, want %d", faceUp, wantIndex)
	}
}

// A room persisted by an older build has no reveal order at all.
func TestRestoreRebuildsAMissingRevealOrder(t *testing.T) {
	r, _, _ := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")
	state := r.PersistedState()
	state.RevealOrder = nil
	state.RevealIndex = 0

	restored, err := RestoreRoom(state, cards.NewSeedDeck())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(restored.revealOrder) != len(restored.submissions) {
		t.Fatalf("rebuilt order has %d slots for %d submissions", len(restored.revealOrder), len(restored.submissions))
	}
	if restored.revealIndex != 1 {
		t.Fatalf("rebuilt reveal index = %d, want 1", restored.revealIndex)
	}
	completeReveal(restored)
	if restored.phase != PhaseJudging {
		t.Fatalf("phase after completing a rebuilt reveal = %q, want judging", restored.phase)
	}
}

func TestScoringRevealsWinnerAndAuthorTogether(t *testing.T) {
	r, judge, textByCard := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")
	completeReveal(r)

	var subID string
	for id := range r.submissions {
		subID = id
		break
	}
	winnerName := r.submissions[subID].PlayerName
	if err := r.PickWinner(judge, subID); err != nil {
		t.Fatalf("pick winner: %v", err)
	}

	snap := r.SnapshotFor(judge)
	winners := 0
	for _, s := range snap.Submissions {
		if !s.Revealed {
			t.Fatalf("scoring left a card face down: %+v", s)
		}
		if s.Answer.Text != textByCard[s.Answer.ID] {
			t.Fatalf("scoring card text = %q, want %q", s.Answer.Text, textByCard[s.Answer.ID])
		}
		// The payoff of blind judging is learning who played what.
		if s.PlayerName == "" {
			t.Fatalf("scoring withheld authorship: %+v", s)
		}
		if s.IsWinner {
			winners++
			if s.ID != subID || s.PlayerName != winnerName {
				t.Fatalf("winner = %q by %q, want %q by %q", s.ID, s.PlayerName, subID, winnerName)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("scoring marked %d winners, want 1", winners)
	}
}

// Every other reveal test drives the steps by hand. This one leaves the room
// alone and checks the deadline it armed, then that the timer behind it fires.
func TestRevealAdvancesOnItsOwnTimer(t *testing.T) {
	r, _, _ := startRoundAndSubmitAll(t, "Alice", "Bob", "Carol", "Dave")

	r.mu.Lock()
	step := revealStepFor(len(r.revealOrder))
	armed := time.Until(r.phaseDeadline)
	startIndex := r.revealIndex
	r.mu.Unlock()

	if armed > step || armed < step-250*time.Millisecond {
		t.Fatalf("reveal armed %v ahead, want about %v", armed, step)
	}

	giveUp := time.Now().Add(step + 2*time.Second)
	for {
		r.mu.Lock()
		index, phase := r.revealIndex, r.phase
		r.mu.Unlock()
		if index > startIndex || phase != PhaseRevealing {
			return
		}
		if time.Now().After(giveUp) {
			t.Fatal("the reveal never advanced on its own; the step timer is not wired up")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
