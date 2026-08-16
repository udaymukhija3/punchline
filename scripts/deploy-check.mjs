#!/usr/bin/env node

import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const args = process.argv.slice(2);
const apiOnly = args.includes('--api-only') || truthy(process.env.PUNCHLINE_API_ONLY);
const targetArg = args.find((arg) => !arg.startsWith('--'));
const baseURL = (targetArg || process.env.PUNCHLINE_BASE_URL || '').replace(/\/$/, '');
const metricsToken = process.env.PUNCHLINE_METRICS_TOKEN || '';
const repoRoot = fileURLToPath(new URL('..', import.meta.url));

if (!baseURL) {
  console.error('usage: node scripts/deploy-check.mjs [--api-only] https://your-punchline-app.example');
  process.exit(2);
}

async function get(path, headers = undefined) {
  const started = Date.now();
  const res = await fetch(`${baseURL}${path}`, { headers });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`${path} returned ${res.status}: ${text.slice(0, 300)}`);
  }
  return { text, ms: Date.now() - started };
}

async function checkAppShell() {
  const html = await get('/');
  if (!html.text.includes('<div id="root">')) {
    throw new Error('/ did not look like the Punchline React shell');
  }
  const assetPaths = [...html.text.matchAll(/(?:src|href)="([^"]*\/assets\/[^"]+)"/g)]
    .map((match) => match[1])
    .filter((value, index, values) => values.indexOf(value) === index);
  if (assetPaths.length === 0) {
    throw new Error('/ did not reference built frontend assets');
  }
  const asset = await get(assetPaths[0]);
  return { htmlMs: html.ms, assetMs: asset.ms, asset: assetPaths[0] };
}

function runSmokeScript(script) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [script, baseURL], {
      cwd: repoRoot,
      stdio: 'inherit',
      env: process.env,
    });
    child.on('exit', (code) => {
      if (code === 0) resolve();
      else reject(new Error(`${script} exited with ${code}`));
    });
    child.on('error', reject);
  });
}

try {
  if (!apiOnly) {
    const app = await checkAppShell();
    console.log(JSON.stringify({ check: 'app_shell', ok: true, ...app }));
  }

  const ready = await get('/readyz');
  console.log(JSON.stringify({ check: 'readyz', ok: true, ms: ready.ms }));

  // A deployment with a database must be serving the database deck. Falling
  // back to the seed file keeps the game playable, which is exactly why it is
  // easy to miss: seed cards carry no database row, so card telemetry and the
  // report button silently stop working. Set PUNCHLINE_EXPECT_DECK_SOURCE=seed
  // for a deliberately database-less deployment.
  const health = JSON.parse((await get('/healthz')).text);
  const expectedDeck = process.env.PUNCHLINE_EXPECT_DECK_SOURCE || 'database';
  if (!String(health.deck_source || '').startsWith(expectedDeck)) {
    throw new Error(`deck_source is ${JSON.stringify(health.deck_source)}, expected ${expectedDeck}`);
  }
  if (expectedDeck === 'database' && !health.content_available) {
    throw new Error('content platform is unavailable on a database deployment');
  }
  console.log(JSON.stringify({
    check: 'health',
    ok: true,
    deck_source: health.deck_source,
    daily_available: health.daily_available,
    content_available: health.content_available,
    admin_enabled: health.admin_enabled,
  }));

  const metrics = await get('/metrics', metricsToken ? { Authorization: `Bearer ${metricsToken}` } : undefined);
  for (const needle of [
    'punchline_http_requests_total',
    'punchline_rooms_local',
    'punchline_players_connected',
    'punchline_ws_active_connections',
    'punchline_instance_draining',
    'punchline_registry_operations_total',
    'punchline_go_heap_alloc_bytes',
    'punchline_daily_actions_total',
    'punchline_daily_worker_errors_total',
    'punchline_daily_worker_last_success_unixtime',
    'punchline_content_actions_total',
    'punchline_cards_auto_retired_total',
    'punchline_card_telemetry_events_total',
  ]) {
    if (!metrics.text.includes(needle)) {
      throw new Error(`/metrics did not include ${needle}`);
    }
  }
  console.log(JSON.stringify({ check: 'metrics', ok: true, ms: metrics.ms }));

  await runSmokeScript('scripts/smoke-realtime.mjs');
  if (!apiOnly || truthy(process.env.PUNCHLINE_CHECK_DAILY)) {
    await runSmokeScript('scripts/smoke-daily.mjs');
  }
  // Exercises reporting and the admin queues. The script skips itself when no
  // admin token is configured, so this is a no-op on a deployment with the desk
  // turned off.
  await runSmokeScript('scripts/smoke-content.mjs');
  console.log(JSON.stringify({ check: 'deploy', ok: true, target: baseURL }));
} catch (err) {
  console.error(JSON.stringify({ check: 'deploy', ok: false, target: baseURL, error: err.message }));
  process.exit(1);
}

function truthy(value) {
  return ['1', 'true', 'yes', 'on'].includes(String(value || '').trim().toLowerCase());
}
