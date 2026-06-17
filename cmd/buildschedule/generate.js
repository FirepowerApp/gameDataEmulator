#!/usr/bin/env node
// generate.js — one-shot Node.js script that runs the same fetch+transform
// logic as cmd/buildschedule, used to produce the initial data file without
// requiring a local Go toolchain.
//
// Usage:
//   node generate.js [--day1 YYYY-MM-DD] [--target-day1 YYYY-MM-DD] [--out PATH]
//   (all flags are optional; defaults match the Go build script)

'use strict';
const https = require('https');
const http  = require('http');
const fs    = require('fs');
const path  = require('path');

// ── Config ──────────────────────────────────────────────────────────────────
const args = Object.fromEntries(
  process.argv.slice(2)
    .join(' ')
    .match(/--(\w[\w-]*)[ =](\S+)/g)
    ?.map(s => { const [k,...v] = s.replace('--','').split(/[ =]/); return [k, v.join('')]; }) ?? []
);

const DAY1        = args['day1']        ?? '2025-10-07';
const TARGET_DAY1 = args['target-day1'] ?? '2026-06-22';
const BASE_URL    = args['base-url']    ?? 'https://api-web.nhle.com';
const RAW_DIR     = args['raw-dir']     ?? path.join('data', 'raw');
const OUT         = args['out']         ?? path.join('internal', 'services', 'data', 'season_2025-26_shifted.json');

const MAX_CONSECUTIVE_EMPTY = 3;
const MAX_WEEKS = 40;

// ── Helpers ──────────────────────────────────────────────────────────────────

function computeOffsetDays(day1, targetDay1) {
  const d1 = new Date(day1 + 'T00:00:00Z');
  const d2 = new Date(targetDay1 + 'T00:00:00Z');
  return Math.round((d2 - d1) / 86400000);
}

// Return NY local time components for a UTC Date.
function toNYLocal(utcDate) {
  const fmt = new Intl.DateTimeFormat('en-US', {
    timeZone: 'America/New_York',
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
    hour12: false,
  });
  const parts = Object.fromEntries(
    fmt.formatToParts(utcDate).map(({ type, value }) => [type, value])
  );
  return {
    year:    parseInt(parts.year,    10),
    month:   parseInt(parts.month,   10),
    day:     parseInt(parts.day,     10),
    hours:   parts.hour === '24' ? 0 : parseInt(parts.hour, 10),
    minutes: parseInt(parts.minute,  10),
    seconds: parseInt(parts.second,  10),
  };
}

// Convert NY local time components back to UTC, DST-aware.
function fromNYLocal({ year, month, day, hours, minutes, seconds }) {
  for (const offsetH of [-4, -5]) {
    const candidate = new Date(Date.UTC(year, month - 1, day, hours - offsetH, minutes, seconds));
    const check = toNYLocal(candidate);
    if (
      check.year === year && check.month === month && check.day === day &&
      check.hours === hours && check.minutes === minutes && check.seconds === seconds
    ) {
      return candidate;
    }
  }
  // Fallback: EDT (-4), used for ambiguous wall-clock times during fall-back transition.
  return new Date(Date.UTC(year, month - 1, day, hours + 4, minutes, seconds));
}

function shiftStartTimeUTC(startTimeUTC, offsetDays) {
  const utc = new Date(startTimeUTC);
  const local = toNYLocal(utc);

  // Normalize the shifted calendar date (local.day + offsetDays may exceed 31).
  // Use Date.UTC with the raw day sum — JavaScript normalises the overflow.
  const shifted = new Date(Date.UTC(local.year, local.month - 1, local.day + offsetDays));

  // Convert shifted NY local time back to UTC (DST-aware: try EDT then EST).
  return fromNYLocal({
    year:    shifted.getUTCFullYear(),
    month:   shifted.getUTCMonth() + 1,
    day:     shifted.getUTCDate(),
    hours:   local.hours,
    minutes: local.minutes,
    seconds: local.seconds,
  }).toISOString().replace('.000Z', 'Z');
}

function shiftDate(dateStr, offsetDays) {
  const d = new Date(dateStr + 'T00:00:00Z');
  d.setUTCDate(d.getUTCDate() + offsetDays);
  return d.toISOString().slice(0, 10);
}

// ── Fetch ────────────────────────────────────────────────────────────────────

function fetchURL(url) {
  return new Promise((resolve, reject) => {
    const mod = url.startsWith('https') ? https : http;
    mod.get(url, res => {
      if (res.statusCode !== 200) {
        reject(new Error(`HTTP ${res.statusCode} for ${url}`));
        return;
      }
      const chunks = [];
      res.on('data', c => chunks.push(c));
      res.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
      res.on('error', reject);
    }).on('error', reject);
  });
}

async function fetchWeek(date, weekNum) {
  fs.mkdirSync(RAW_DIR, { recursive: true });
  const cachePath = path.join(RAW_DIR, `2025-W${String(weekNum).padStart(2,'0')}-${date}.json`);
  if (fs.existsSync(cachePath)) {
    process.stderr.write(`  week ${weekNum} ${date}: cache hit\n`);
    return JSON.parse(fs.readFileSync(cachePath, 'utf8'));
  }
  process.stderr.write(`  week ${weekNum} ${date}: fetching...\n`);
  const body = await fetchURL(`${BASE_URL}/v1/schedule/${date}`);
  fs.writeFileSync(cachePath, body);
  return JSON.parse(body);
}

async function fetchSeason() {
  const allDays = [];
  let date = DAY1;
  let consecutiveEmpty = 0;

  for (let week = 1; week <= MAX_WEEKS; week++) {
    const resp = await fetchWeek(date, week);
    const gameWeek = resp.gameWeek ?? [];
    let type2Count = 0;
    for (const day of gameWeek) {
      allDays.push(day);
      for (const g of (day.games ?? [])) {
        if (g.gameType === 2) type2Count++;
      }
    }
    if (type2Count === 0) {
      consecutiveEmpty++;
      if (consecutiveEmpty >= MAX_CONSECUTIVE_EMPTY) break;
    } else {
      consecutiveEmpty = 0;
    }
    if (!resp.nextStartDate) break;
    date = resp.nextStartDate;
  }
  return allDays;
}

// ── Transform ────────────────────────────────────────────────────────────────

function transformSeason(rawDays, offsetDays) {
  const byDate = {};
  for (const day of rawDays) {
    for (const g of (day.games ?? [])) {
      if (g.gameType !== 2) continue; // D2: regular-season only
      const shiftedDate  = shiftDate(day.date, offsetDays);
      const shiftedStart = shiftStartTimeUTC(g.startTimeUTC, offsetDays);
      const out = {
        id:           g.id,
        gameDate:     shiftedDate,
        startTimeUTC: shiftedStart,
        gameState:    'FUT',         // D1: force FUT so scheduler enqueues it
        gameType:     g.gameType,
        homeTeam: {
          id:                       g.homeTeam.id,
          commonName:               g.homeTeam.commonName,
          placeName:                g.homeTeam.placeName,
          placeNameWithPreposition: g.homeTeam.placeNameWithPreposition,
          abbrev:                   g.homeTeam.abbrev,
        },
        awayTeam: {
          id:                       g.awayTeam.id,
          commonName:               g.awayTeam.commonName,
          placeName:                g.awayTeam.placeName,
          placeNameWithPreposition: g.awayTeam.placeNameWithPreposition,
          abbrev:                   g.awayTeam.abbrev,
        },
      };
      byDate[shiftedDate] = byDate[shiftedDate] ?? [];
      byDate[shiftedDate].push(out);
    }
  }

  const dates = Object.keys(byDate).sort();
  const gameWeek = dates.map(date => {
    const games = byDate[date].sort((a, b) => a.id - b.id);
    return { date, games };
  });
  return { gameWeek };
}

// ── Main ─────────────────────────────────────────────────────────────────────

(async () => {
  const offsetDays = computeOffsetDays(DAY1, TARGET_DAY1);
  process.stderr.write(`Shifting ${DAY1} → ${TARGET_DAY1} (${offsetDays} days)\n`);

  const rawDays = await fetchSeason();
  process.stderr.write(`Fetched ${rawDays.length} day-entries\n`);

  const result = transformSeason(rawDays, offsetDays);
  const gameCount = result.gameWeek.reduce((n, d) => n + d.games.length, 0);
  process.stderr.write(`Produced ${gameCount} regular-season games across ${result.gameWeek.length} days\n`);

  const outDir = path.dirname(OUT);
  fs.mkdirSync(outDir, { recursive: true });
  fs.writeFileSync(OUT, JSON.stringify(result, null, 2));
  process.stderr.write(`Wrote ${OUT}\n`);
})().catch(err => { process.stderr.write(`ERROR: ${err.message}\n`); process.exit(1); });
