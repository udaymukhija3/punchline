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

function runSmoke() {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ['scripts/smoke-realtime.mjs', baseURL], {
      cwd: repoRoot,
      stdio: 'inherit',
      env: process.env,
    });
    child.on('exit', (code) => {
      if (code === 0) resolve();
      else reject(new Error(`smoke exited with ${code}`));
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

  const metrics = await get('/metrics', metricsToken ? { Authorization: `Bearer ${metricsToken}` } : undefined);
  for (const needle of [
    'punchline_http_requests_total',
    'punchline_rooms_local',
    'punchline_players_connected',
    'punchline_ws_active_connections',
    'punchline_instance_draining',
    'punchline_registry_operations_total',
    'punchline_go_heap_alloc_bytes',
  ]) {
    if (!metrics.text.includes(needle)) {
      throw new Error(`/metrics did not include ${needle}`);
    }
  }
  console.log(JSON.stringify({ check: 'metrics', ok: true, ms: metrics.ms }));

  await runSmoke();
  console.log(JSON.stringify({ check: 'deploy', ok: true, target: baseURL }));
} catch (err) {
  console.error(JSON.stringify({ check: 'deploy', ok: false, target: baseURL, error: err.message }));
  process.exit(1);
}

function truthy(value) {
  return ['1', 'true', 'yes', 'on'].includes(String(value || '').trim().toLowerCase());
}
