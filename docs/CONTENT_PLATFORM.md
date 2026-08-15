# Content platform

How cards get into Punchline, how they earn their place, and how they leave.

There is no AI generation step. New cards come from players: daily rounds are a
continuous writers' room, and the answers a group actually voted for become
candidates for the shared deck. Every community card was therefore written by a
person and play-tested by their friends before an admin ever saw it.

## Where cards live

`seed/cards.json` is the authoring format for the launch deck. Migration
`005_seed_official_pack.sql` loads it into `packs` / `prompt_cards` /
`answer_cards`; from then on the running process reads cards from Postgres and
each card carries its row id.

Two packs ship:

| Pack | Slug | Source |
| --- | --- | --- |
| Punchline Core | `official-core` | The launch deck |
| From the Daily | `community-daily` | Player answers promoted by an admin |

`PUNCHLINE_DECK_SOURCE` controls the loader: `auto` (default) prefers Postgres
and falls back to the seed file, `database` refuses to start without a usable
database deck, `seed` ignores the database entirely.

The loader only admits `approved` cards from `approved` packs, and only at the
`family` and `party` tiers. `unfiltered` cards are excluded deliberately:
`Deck.For` treats any non-family tier as "everything", so admitting them would
leak them into party rooms.

## The pipeline

```
daily round finalizes
  └─ answers with >= 2 votes  ──►  card_candidates (pending, deduped)
                                        │
                                   admin approves
                                        │
                                        ▼
                              answer_cards in community-daily
                                   (approved, tier chosen by admin)
```

Details that matter:

- **Dedupe.** `normalized_text` (lowercased, punctuation stripped, whitespace
  collapsed) is unique, so the same joke from twenty groups is one queue entry.
- **Cap.** At most three candidates per round, so one large group cannot flood
  the queue.
- **Tier.** Candidates default to `party`. Player-written text is never assumed
  safe for family rooms; an admin must explicitly approve it as `family`.
- **Withdrawal.** Candidates cascade-delete with their source round, so deleting
  a daily group also withdraws its un-promoted content.

## Telemetry

Cards are counted as they play. Events are buffered in memory and flushed in
batched updates every `CARD_TELEMETRY_FLUSH_INTERVAL`; if the buffer fills, the
event is dropped and counted rather than blocking a room. A lost statistic is
cheaper than a stalled game.

| Column | Incremented when |
| --- | --- |
| `prompt_cards.times_played` | A round begins with that prompt (live or daily) |
| `prompt_cards.skip_count` | The host skips that prompt |
| `answer_cards.times_played` | A player submits that card |
| `answer_cards.win_count` | The judge picks that card |

The admin card browser sorts on these, which is the retirement workflow: sort
answers by fewest plays or prompts by most skips, and cut what is not working.

## Reporting and moderation

Any player can report the prompt or a revealed answer from inside a room. No
account is required — reporters are identified only by a salted hash of their
address, used to dedupe.

- Reports are **one per card per reporter**; filing repeatedly updates the
  existing row rather than stacking.
- At **3 distinct reporters** a card is retired automatically and stops being
  dealt. Waiting for someone to check a queue is too slow for a card that is
  genuinely offensive.
- An admin resolves reports per card, not per report: `retired` keeps it out,
  `dismissed` restores it and clears the count.

`punchline_cards_auto_retired_total` is the metric to alert on — a sudden rise
means either an abuse campaign or a bad batch of content, and both need a look.

## Admin desk

`/admin`, gated by `PUNCHLINE_ADMIN_TOKEN`. With the variable unset every admin
route returns 404 and the desk does not exist. The token is compared in constant
time and never logged.

The product has no user accounts, so there is no role system and **no user ban**.
The moderation levers are content-level: retire a card, reject a candidate, or
delete a single daily submission (`DELETE /api/admin/daily-submissions/{id}`).
A real ban would require accounts, which Punchline does not have.

| Route | Purpose |
| --- | --- |
| `GET /api/admin/overview` | Queue depths and deck size |
| `GET/POST /api/admin/reports` | Report queue; resolve per card |
| `GET/POST /api/admin/candidates` | Candidate queue; approve or reject |
| `GET/POST /api/admin/cards` | Card browser with telemetry; retire or restore |
| `POST /api/cards/report` | Public; used by players in a room |

## Editing the launch deck

Migration 005 is generated, not hand-written:

```bash
node scripts/generate-card-seed-migration.mjs
```

CI regenerates it and fails on drift. Once it has been applied anywhere the
runner rejects edits by checksum, so later content changes belong in a new
migration or in the admin desk.
