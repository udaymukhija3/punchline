#!/usr/bin/env node

const target = (process.argv[2] || process.env.PUNCHLINE_BASE_URL || 'http://127.0.0.1:8080').replace(/\/$/, '');
const suffix = Date.now().toString(36).slice(-6);
const timezone = submissionTimezone();

function submissionTimezone() {
  // Use a real IANA zone (not UTC) whose current local time is safely before
  // the default 18:00 reveal. This keeps the smoke deterministic at any UTC
  // hour while proving that the production binary contains timezone data.
  const zones = [
    'Pacific/Pago_Pago',
    'America/Los_Angeles',
    'America/New_York',
    'Europe/London',
    'Asia/Kolkata',
    'Asia/Tokyo',
    'Pacific/Auckland',
  ];
  return zones.find((zone) => {
    const hour = Number(new Intl.DateTimeFormat('en-US', {
      timeZone: zone,
      hour: '2-digit',
      hourCycle: 'h23',
    }).format(new Date()));
    return hour >= 1 && hour <= 16;
  }) || 'America/Los_Angeles';
}

async function request(path, { method = 'GET', token, body, expected = [200, 201, 204] } = {}) {
  const started = Date.now();
  const headers = {};
  if (body) headers['Content-Type'] = 'application/json';
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`${target}${path}`, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const text = await res.text();
  const data = text ? JSON.parse(text) : {};
  if (!expected.includes(res.status)) {
    throw new Error(`${method} ${path} returned ${res.status}: ${text.slice(0, 300)}`);
  }
  return { data, status: res.status, ms: Date.now() - started };
}

let owner;
try {
  const created = await request('/api/daily/groups', {
    method: 'POST',
    body: { name: `Deploy check ${suffix}`, player_name: 'Smoke Host', timezone },
  });
  owner = created.data;
  if (!owner?.group?.code || !owner?.token || !owner?.membership?.id) {
    throw new Error('daily group creation returned an incomplete session');
  }
  const code = owner.group.code;
  const joined = await request(`/api/daily/groups/${code}/join`, {
    method: 'POST', body: { player_name: 'Smoke Friend' },
  });
  if (!joined.data?.token) throw new Error('daily join returned no token');

  await request(`/api/daily/groups/${code}/today`, { token: 'invalid-token', expected: [401] });
  const today = await request(`/api/daily/groups/${code}/today`, { token: owner.token });
  if (today.data?.today?.state !== 'OPEN_FOR_SUBMISSIONS' || !today.data?.today?.prompt) {
    throw new Error(`unexpected daily round: ${JSON.stringify(today.data?.today)}`);
  }
  const roundID = today.data.today.id;
  await request(`/api/daily/rounds/${roundID}/submit`, {
    method: 'POST', token: owner.token, body: { answer_text: `Smoke answer ${suffix}` },
  });
  await request(`/api/daily/rounds/${roundID}/submit`, {
    method: 'POST', token: owner.token, body: { answer_text: `Updated smoke answer ${suffix}` },
  });
  const friendView = await request(`/api/daily/groups/${code}/today`, { token: joined.data.token });
  if (friendView.data.today.submissions?.length) {
    throw new Error('open daily round leaked another member submission');
  }

  console.log(JSON.stringify({
    ok: true,
    target,
    code,
    round: today.data.today.number,
    state: today.data.today.state,
    timezone,
    timings: { createMs: created.ms, joinMs: joined.ms, todayMs: today.ms },
  }, null, 2));
} finally {
  if (owner?.group?.code && owner?.token) {
    await request(`/api/daily/groups/${owner.group.code}`, { method: 'DELETE', token: owner.token }).catch((err) => {
      console.error(`daily smoke cleanup failed: ${err.message}`);
      process.exitCode = 1;
    });
  }
}
