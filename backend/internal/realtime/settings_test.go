package realtime

import (
	"testing"

	"punchline/backend/internal/cards"
)

func TestUpdateSettingsHostOnlyLobbyOnlyAndClamped(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	guest := r.Join("Bob")
	r.Join("Carol")

	if err := r.UpdateSettings(guest.ID, &Settings{ScoreLimit: 7}); err == nil {
		t.Fatal("non-host was allowed to change settings")
	}

	if err := r.UpdateSettings(host.ID, &Settings{ScoreLimit: 7, RoundSeconds: 60, MaxPlayers: 8}); err != nil {
		t.Fatalf("host update failed: %v", err)
	}
	snap := r.SnapshotFor("")
	if snap.ScoreLimit != 7 || snap.RoundSeconds != 60 || snap.MaxPlayers != 8 {
		t.Fatalf("settings not applied: limit=%d secs=%d max=%d", snap.ScoreLimit, snap.RoundSeconds, snap.MaxPlayers)
	}

	// Out-of-range values clamp; zero fields are left unchanged.
	if err := r.UpdateSettings(host.ID, &Settings{ScoreLimit: 999}); err != nil {
		t.Fatal(err)
	}
	snap = r.SnapshotFor("")
	if snap.ScoreLimit != maxScoreLimit {
		t.Fatalf("score limit not clamped: %d", snap.ScoreLimit)
	}
	if snap.RoundSeconds != 60 {
		t.Fatalf("zero field should keep round seconds, got %d", snap.RoundSeconds)
	}

	// Settings are locked once the game starts.
	if err := r.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateSettings(host.ID, &Settings{ScoreLimit: 5}); err == nil {
		t.Fatal("settings changed outside the lobby")
	}
}

func TestUpdateSettingsFamilyTierFiltersDeckAndHands(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	r.Join("Carol")

	if err := r.UpdateSettings(host.ID, &Settings{ContentTier: cards.TierFamily}); err != nil {
		t.Fatal(err)
	}
	if snap := r.SnapshotFor(""); snap.ContentTier != cards.TierFamily {
		t.Fatalf("tier = %q, want family", snap.ContentTier)
	}
	for _, a := range r.deck.Answers {
		if a.Tier != cards.TierFamily {
			t.Fatalf("non-family card %q in family deck", a.ID)
		}
	}
	// Hands are re-dealt on a tier change, so they must be family-only too.
	self := r.SnapshotFor(host.ID)
	for _, p := range self.Players {
		if p.ID != host.ID {
			continue
		}
		if len(p.Hand) != handSize {
			t.Fatalf("hand size = %d, want %d", len(p.Hand), handSize)
		}
		for _, c := range p.Hand {
			if c.Tier != cards.TierFamily {
				t.Fatalf("family room dealt non-family card %q", c.ID)
			}
		}
	}
}

func TestIsFullRespectsMaxPlayers(t *testing.T) {
	r := newTestRoom()
	host := r.Join("Alice")
	r.Join("Bob")
	r.Join("Carol")

	if r.IsFull() {
		t.Fatal("room should not be full at 3/12")
	}
	if err := r.UpdateSettings(host.ID, &Settings{MaxPlayers: 3}); err != nil {
		t.Fatal(err)
	}
	if !r.IsFull() {
		t.Fatal("room should be full at 3/3")
	}
}
