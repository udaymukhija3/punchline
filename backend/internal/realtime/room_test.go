package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"punchline/backend/internal/cards"
)

func newTestRoom() *Room {
	return NewRoom("TEST", cards.NewSeedDeck())
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
	if r.phase != PhaseJudging {
		t.Fatalf("phase after computer submit = %q, want judging", r.phase)
	}
	r.pickComputerWinner(r.judgeID, r.phaseSeq)
	if r.phase != PhaseScoring {
		t.Fatalf("phase after computer judge = %q, want scoring", r.phase)
	}
}

func TestTryJoinRejectsFullOrStartedRoom(t *testing.T) {
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

	r.maxPlayers = 12
	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TryJoin("Eve"); !errors.Is(err, ErrRoomAlreadyStarted) {
		t.Fatalf("err = %v, want ErrRoomAlreadyStarted", err)
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
	if r.phase != PhaseJudging {
		t.Fatalf("phase = %q after all answerers submitted, want judging", r.phase)
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
