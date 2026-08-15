// Package telemetry counts how cards actually perform in play. The counts feed
// the admin desk's retirement decisions: a card that gets skipped constantly is
// a card to cut, and one that keeps winning is one to keep.
//
// Recording must never slow a game down. Events go into a fixed buffer and are
// aggregated by a background flusher; if the buffer is full the event is
// dropped and counted, because a lost statistic is always cheaper than a
// stalled room.
package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// Metric names. Prompts can be played and skipped; answers can be played and
// won. Each maps to a counter column on the card tables.
const (
	MetricPlayed  = "played"
	MetricSkipped = "skipped"
	MetricWon     = "won"
)

const (
	KindPrompt = "prompt"
	KindAnswer = "answer"
)

type event struct {
	kind   string
	uuid   string
	metric string
}

// Recorder is the interface the game engines depend on. Nil-safe: a nil
// *CardRecorder discards everything, which is what a seed-backed deck wants.
type Recorder interface {
	PromptPlayed(uuid string)
	PromptSkipped(uuid string)
	AnswerPlayed(uuid string)
	AnswerWon(uuid string)
}

// CardRecorder batches card counter updates into periodic writes.
type CardRecorder struct {
	db     *sql.DB
	events chan event

	mu      sync.Mutex
	flushed int64
	dropped int64
	failed  int64
}

// NewCardRecorder returns a recorder that writes to db. A nil db yields a nil
// recorder, which is still safe to call.
func NewCardRecorder(db *sql.DB, buffer int) *CardRecorder {
	if db == nil {
		return nil
	}
	if buffer <= 0 {
		buffer = 4096
	}
	return &CardRecorder{db: db, events: make(chan event, buffer)}
}

func (r *CardRecorder) PromptPlayed(uuid string)  { r.record(KindPrompt, uuid, MetricPlayed) }
func (r *CardRecorder) PromptSkipped(uuid string) { r.record(KindPrompt, uuid, MetricSkipped) }
func (r *CardRecorder) AnswerPlayed(uuid string)  { r.record(KindAnswer, uuid, MetricPlayed) }
func (r *CardRecorder) AnswerWon(uuid string)     { r.record(KindAnswer, uuid, MetricWon) }

// record never blocks. Cards loaded from the seed file have no database row, so
// an empty uuid is simply ignored.
func (r *CardRecorder) record(kind, uuid, metric string) {
	if r == nil || uuid == "" {
		return
	}
	select {
	case r.events <- event{kind: kind, uuid: uuid, metric: metric}:
	default:
		r.mu.Lock()
		r.dropped++
		r.mu.Unlock()
	}
}

// Stats reports cumulative counters for the metrics endpoint.
func (r *CardRecorder) Stats() (flushed, dropped, failed int64) {
	if r == nil {
		return 0, 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushed, r.dropped, r.failed
}

// Start runs the flush loop until ctx is cancelled, then drains what is already
// buffered so a clean shutdown does not lose the last few rounds.
func (r *CardRecorder) Start(ctx context.Context, interval time.Duration) {
	if r == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		pending := map[event]int{}
		for {
			select {
			case <-ctx.Done():
				r.drain(pending)
				r.flush(context.Background(), pending)
				return
			case ev := <-r.events:
				pending[ev]++
				// Opportunistically batch whatever else is already queued.
				r.drain(pending)
			case <-ticker.C:
				r.flush(ctx, pending)
			}
		}
	}()
}

func (r *CardRecorder) drain(pending map[event]int) {
	for {
		select {
		case ev := <-r.events:
			pending[ev]++
		default:
			return
		}
	}
}

// flush writes one statement per (kind, metric) group, each updating many rows
// at once. Failures are counted and the batch is dropped rather than retried
// forever: these are statistics, not state.
func (r *CardRecorder) flush(ctx context.Context, pending map[event]int) {
	if len(pending) == 0 {
		return
	}
	type key struct{ kind, metric string }
	groups := map[key]map[string]int{}
	for ev, count := range pending {
		k := key{kind: ev.kind, metric: ev.metric}
		if groups[k] == nil {
			groups[k] = map[string]int{}
		}
		groups[k][ev.uuid] += count
	}
	for k := range pending {
		delete(pending, k)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for k, counts := range groups {
		table, column, err := columnFor(k.kind, k.metric)
		if err != nil {
			log.Printf("card telemetry: %v", err)
			continue
		}
		ids := make([]string, 0, len(counts))
		increments := make([]int64, 0, len(counts))
		var total int64
		for uuid, count := range counts {
			ids = append(ids, uuid)
			increments = append(increments, int64(count))
			total += int64(count)
		}
		// Column names come from columnFor's fixed table, never from input.
		query := fmt.Sprintf(`
			UPDATE %s AS c SET %s = c.%s + v.increment
			FROM (SELECT unnest($1::text[])::uuid AS id, unnest($2::bigint[]) AS increment) AS v
			WHERE c.id = v.id`, table, column, column)
		if _, err := r.db.ExecContext(writeCtx, query, ids, increments); err != nil {
			log.Printf("card telemetry flush: %v", err)
			r.mu.Lock()
			r.failed += total
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		r.flushed += total
		r.mu.Unlock()
	}
}

func columnFor(kind, metric string) (table, column string, err error) {
	switch {
	case kind == KindPrompt && metric == MetricPlayed:
		return "prompt_cards", "times_played", nil
	case kind == KindPrompt && metric == MetricSkipped:
		return "prompt_cards", "skip_count", nil
	case kind == KindAnswer && metric == MetricPlayed:
		return "answer_cards", "times_played", nil
	case kind == KindAnswer && metric == MetricWon:
		return "answer_cards", "win_count", nil
	default:
		return "", "", fmt.Errorf("unknown card metric %s/%s", kind, metric)
	}
}
