# PUNCHLINE — Artifact Pack

PUNCHLINE is a web-first, real-time multiplayer party game platform that starts with a buttery live room engine and later expands into async daily rounds and an AI-assisted content platform.

This pack is intentionally build-oriented. It is designed to help you turn the idea into a portfolio-grade product without drowning in product bloat.

## Recommended build order

1. **v0 Live Game Engine** — rooms, guest players, WebSockets, state machine, submissions, judging, scoring, reconnect.
2. **v1 Daily / Async Engine** — one prompt per group/day, submissions, reveal, voting, streaks, share card.
3. **v2 Content Platform** — card packs, AI generation jobs, moderation, ratings, safety tiers.

## Files

- `docs/01_product_brief.md` — clean product framing.
- `docs/02_mvp_scope.md` — ruthless v0/v1/v2 scope.
- `docs/03_architecture.md` — backend/frontend/system architecture.
- `docs/04_realtime_engine.md` — room actor and WebSocket model.
- `docs/05_async_daily_engine.md` — daily mode model.
- `docs/06_content_platform.md` — AI/UGC content engine.
- `docs/07_safety_moderation.md` — safety as product surface.
- `docs/08_build_plan.md` — practical implementation sequence.
- `specs/api_contract.md` — REST API surface.
- `specs/websocket_contract.md` — WebSocket messages and room events.
- `schemas/postgres_schema.sql` — starter relational schema.
- `resume/resume_positioning.md` — resume bullets and project summary.
- `prompts/codex_build_prompt.md` — prompt to start implementation with Codex.
- `prompts/claude_review_prompt.md` — prompt to review architecture and code.
