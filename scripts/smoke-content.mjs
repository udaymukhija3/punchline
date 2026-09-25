#!/usr/bin/env node
// Exercises the content platform over HTTP: player reporting, the admin
// queues, and moderation actions. Requires PUNCHLINE_ADMIN_TOKEN to match the
// target deployment; without it the admin desk is off and this is skipped.

const target = (process.argv[2] || process.env.PUNCHLINE_BASE_URL || 'http://127.0.0.1:8080').replace(/\/$/, '');
const adminToken = process.env.PUNCHLINE_ADMIN_TOKEN || '';

if (!adminToken) {
  console.log(JSON.stringify({ check: 'content', skipped: 'PUNCHLINE_ADMIN_TOKEN is not set' }));
  process.exit(0);
}

async function request(path, { method = 'GET', token, body, expected = [200, 201, 204] } = {}) {
  const headers = {};
  if (body) headers['Content-Type'] = 'application/json';
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`${target}${path}`, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const text = await res.text();
  const data = text ? JSON.parse(text) : {};
  if (!expected.includes(res.status)) {
    throw new Error(`${method} ${path} returned ${res.status}: ${text.slice(0, 300)}`);
  }
  return { data, status: res.status };
}

try {
  // The desk must be closed to anyone without the token.
  await request('/api/admin/overview', { expected: [401] });
  await request('/api/admin/overview', { token: 'not-the-token', expected: [401] });
  await request('/api/admin/cards', { expected: [401] });

  const overview = await request('/api/admin/overview', { token: adminToken });
  if (typeof overview.data.approved_answers !== 'number' || overview.data.approved_answers < 1) {
    throw new Error(`overview reports no live answers: ${JSON.stringify(overview.data)}`);
  }

  const listed = await request('/api/admin/cards?kind=answer&status=approved&limit=1', { token: adminToken });
  const card = listed.data.cards?.[0];
  if (!card?.id) throw new Error('admin card browser returned nothing to report');

  // Report from a seat in a fresh room, the way a player does. A reporter gets
  // one say per card, so reporting by address would make every rerun from the
  // same machine a repeat of a report this script already dismissed.
  const room = await request('/api/rooms', { method: 'POST' });
  const seat = await request(`/api/rooms/${room.data.code}/join`, { method: 'POST', body: { name: 'Smoke' } });
  const report = {
    card_kind: 'answer', card_id: card.id, reason: 'broken', detail: 'deploy smoke',
    room_code: room.data.code, player_id: seat.data.player.id, token: seat.data.token,
  };
  const reported = await request('/api/cards/report', { method: 'POST', body: report });
  if (!reported.data.recorded) throw new Error('report was not recorded');

  // Malformed reports must be rejected, not silently accepted.
  await request('/api/cards/report', {
    method: 'POST',
    body: { card_kind: 'answer', card_id: card.id, reason: 'not-a-reason' },
    expected: [400],
  });

  const queue = await request('/api/admin/reports?limit=100', { token: adminToken });
  if (!(queue.data.reports || []).some((report) => report.card_id === card.id)) {
    throw new Error('the filed report is missing from the admin queue');
  }

  await request('/api/admin/reports', {
    method: 'POST', token: adminToken,
    body: { card_kind: 'answer', card_id: card.id, resolution: 'dismissed' },
  });

  const after = await request('/api/admin/cards?kind=answer&status=approved&limit=200', { token: adminToken });
  if (!(after.data.cards || []).some((item) => item.id === card.id)) {
    throw new Error('the dismissed card did not return to rotation');
  }

  // Re-filing after review must say so rather than claim it was recorded.
  const refiled = await request('/api/cards/report', { method: 'POST', body: report });
  if (refiled.data.recorded || !refiled.data.already_reviewed) {
    throw new Error(`re-reporting a reviewed card answered ${JSON.stringify(refiled.data)}`);
  }

  const candidates = await request('/api/admin/candidates', { token: adminToken });
  if (!Array.isArray(candidates.data.candidates)) throw new Error('candidate queue is not a list');

  console.log(JSON.stringify({
    check: 'content',
    ok: true,
    target,
    live_answers: overview.data.approved_answers,
    live_prompts: overview.data.approved_prompts,
    pending_candidates: overview.data.pending_candidates,
    auto_retire_at: overview.data.auto_retire_at,
  }, null, 2));
} catch (err) {
  console.error(JSON.stringify({ check: 'content', ok: false, target, error: err.message }));
  process.exit(1);
}
