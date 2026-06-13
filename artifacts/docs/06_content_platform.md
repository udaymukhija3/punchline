# Content Platform

## Purpose

The content platform is what turns PUNCHLINE from a clone into a living game service.

Do not build this before the real-time engine works.

## Core objects

```txt
Pack
Card
GenerationJob
CardRating
CardReport
ModerationAction
PackInstall
```

## Card fields

Each card should know:

- type: prompt or answer
- text
- source: official/original, AI, community, topical
- tier: family, party, unfiltered
- status: draft, approved, rejected, retired
- pack_id
- author_id nullable
- generation_job_id nullable
- play_count
- win_count
- skip_count
- report_count

## AI generation pipeline

```txt
Theme submitted
→ generation job created
→ prompt sent to LLM
→ candidate cards generated
→ automated safety filter
→ duplicate/similarity check
→ draft pack created
→ admin/community review
→ cards approved
→ pack published
```

## Quality signals

Track these per card:

- times played
- times submitted
- win rate
- skip rate
- ban/report rate
- average rating

A card with high report rate or skip rate should be retired automatically or sent to moderation.

## Content moat

The moat is not “LLM generated jokes.”

The moat is:

> Generate many candidates, filter aggressively, learn from play data, and promote what actually works.

## v2 implementation advice

Start with manual/admin approval. Full automation can come later.
