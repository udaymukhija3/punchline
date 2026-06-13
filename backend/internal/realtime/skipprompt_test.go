package realtime

import "testing"

func TestSkipPromptReturnsCardsAndResets(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	bob := r.Join("Bob")
	r.Join("Carol")
	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	firstPrompt := r.prompt.ID

	// A non-judge plays a card.
	submitter := bob.ID
	if submitter == r.judgeID {
		submitter = host.ID
	}
	card := r.players[submitter].Hand[0]
	handBefore := len(r.players[submitter].Hand)
	if err := r.SubmitAnswer(submitter, card.ID); err != nil {
		t.Fatal(err)
	}

	// Non-host cannot skip (Bob is never the host — Alice joined first).
	if err := r.SkipPrompt(bob.ID); err == nil {
		t.Fatal("non-host was allowed to skip the prompt")
	}

	if err := r.SkipPrompt(host.ID); err != nil {
		t.Fatalf("host skip failed: %v", err)
	}
	if r.phase != PhaseSubmitting {
		t.Fatalf("phase = %q after skip, want submitting", r.phase)
	}
	if len(r.submissions) != 0 {
		t.Fatalf("submissions not cleared after skip: %d", len(r.submissions))
	}
	if got := len(r.players[submitter].Hand); got != handBefore {
		t.Fatalf("submitted card not returned to hand: have %d, want %d", got, handBefore)
	}
	if r.prompt.ID == firstPrompt {
		// possible but unlikely with a 30-card pile; only fail if it never differs
		t.Logf("note: skip drew the same prompt id %q", firstPrompt)
	}
}

func TestSnapshotReportsSubmittedFlag(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	bob := r.Join("Bob")
	r.Join("Carol")
	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	submitter := bob.ID
	if submitter == r.judgeID {
		submitter = host.ID
	}
	card := r.players[submitter].Hand[0]
	if err := r.SubmitAnswer(submitter, card.ID); err != nil {
		t.Fatal(err)
	}
	snap := r.SnapshotFor("")
	for _, p := range snap.Players {
		want := p.ID == submitter
		if p.Submitted != want {
			t.Fatalf("player %s submitted=%v, want %v", p.Name, p.Submitted, want)
		}
	}
}
