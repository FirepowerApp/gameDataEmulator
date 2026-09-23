#!/usr/bin/env node
// generate.js — one-shot Node.js script that runs the same fetch+transform
// logic as cmd/buildschedule, used to produce the initial data file without
// requiring a local Go toolchain.
//
// Produces the canonical (unshifted, real calendar dates) season JSON — no
// date math happens here. The emulator rebases this data onto the
// requesting offseason day at runtime (see internal/services/schedule.go).
//
// Usage:
//   node generate.js [--day1 YYYY-MM-DD] [--out PATH]
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

const DAY1     = args['day1']     ?? '2025-10-07';
const BASE_URL = args['base-url'] ?? 'https://api-web.nhle.com';
const RAW_DIR  = args['raw-dir']  ?? path.join('data', 'raw');
const OUT      = args['out']      ?? path.join('internal', 'services', 'data', 'season_2025-26.json');

const MAX_CONSECUTIVE_EMPTY = 3;
const MAX_WEEKS = 40;

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
// Filters to regular-season games, forces GameState=FUT (the completed
// season returns OFF; scheduler.go skips non-FUT games), and groups by the
// real calendar date. No shifting — real dates in, real dates out.

function transformSeason(rawDays) {
  const byDate = {};
  for (const day of rawDays) {
    for (const g of (day.games ?? [])) {
      if (g.gameType !== 2) continue; // D2: regular-season only
      const out = {
        id:           g.id,
        gameDate:     day.date,
        startTimeUTC: g.startTimeUTC,
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
      byDate[day.date] = byDate[day.date] ?? [];
      byDate[day.date].push(out);
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
  process.stderr.write(`Fetching season schedule from ${BASE_URL} starting ${DAY1}\n`);

  const rawDays = await fetchSeason();
  process.stderr.write(`Fetched ${rawDays.length} day-entries\n`);

  const result = transformSeason(rawDays);
  const gameCount = result.gameWeek.reduce((n, d) => n + d.games.length, 0);
  process.stderr.write(`Produced ${gameCount} regular-season games across ${result.gameWeek.length} days\n`);

  const outDir = path.dirname(OUT);
  fs.mkdirSync(outDir, { recursive: true });
  fs.writeFileSync(OUT, JSON.stringify(result, null, 2));
  process.stderr.write(`Wrote ${OUT}\n`);
})().catch(err => { process.stderr.write(`ERROR: ${err.message}\n`); process.exit(1); });
