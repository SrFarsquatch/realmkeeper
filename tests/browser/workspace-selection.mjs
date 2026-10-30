// Browser regression test for server selection and workspace isolation.
//
// It starts a real Realmkeeper binary against a throwaway data directory, creates a
// Hytale server backed by a fake start.sh (no game download), a Minecraft Java
// server and a Dragonwilds server, then drives the UI in Chromium.
//
// Usage:
//   go build -o /tmp/realmkeeper ./cmd/realmkeeper
//   node tests/browser/workspace-selection.mjs /tmp/realmkeeper
//
// Playwright is resolved from PLAYWRIGHT_MODULE when set, otherwise from the
// "playwright" package (npm install playwright).
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import assert from 'node:assert/strict';

const binary = process.argv[2];
if (!binary) {
  console.error('usage: node tests/browser/workspace-selection.mjs <path-to-realmkeeper-binary>');
  process.exit(2);
}
const playwrightModule = process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : 'playwright';
const { chromium } = await import(playwrightModule);

const port = 18000 + Math.floor(Math.random() * 1000);
const base = `http://127.0.0.1:${port}`;
const dataDir = mkdtempSync(join(tmpdir(), 'realmkeeper-browser-'));
// A stand-in `java` so Minecraft servers can run without Mojang's jar.
const fakeBin = mkdtempSync(join(tmpdir(), 'realmkeeper-fake-java-'));
writeFileSync(join(fakeBin, 'java'), [
  '#!/bin/bash',
  'if [ "$1" = "-version" ]; then echo \'openjdk version "21.0.4"\' >&2; exit 0; fi',
  'echo \'[Server thread/INFO]: Done (1.0s)! For help, type "help"\'',
  'while read -r line; do',
  '  echo "[Server thread/INFO]: got $line"',
  '  if [ "$line" = "save-all flush" ]; then echo "[Server thread/INFO]: Saved the game"; fi',
  '  if [ "$line" = "list" ]; then echo "[Server thread/INFO]: There are 2 of a max of 20 players online: Alex, Steve"; fi',
  // Like the real server, op writes ops.json in the working directory.
  '  if [ "${line%% *}" = "op" ]; then echo "[{\\"uuid\\":\\"u\\",\\"name\\":\\"${line#op }\\",\\"level\\":4}]" > ops.json; fi',
  '  if [ "$line" = "stop" ]; then exit 0; fi',
  'done',
].join('\n'), { mode: 0o755 });
const server = spawn(binary, [], {
  env: { ...process.env, PATH: fakeBin + ':' + process.env.PATH, REALMKEEPER_DATA_DIR: dataDir, REALMKEEPER_LISTEN: `127.0.0.1:${port}`, REALMKEEPER_GAME_PORT_START: '5520', REALMKEEPER_GAME_PORT_END: '5525' },
  stdio: ['ignore', 'pipe', 'pipe'],
});
let serverOutput = '';
server.stdout.on('data', d => { serverOutput += d; });
server.stderr.on('data', d => { serverOutput += d; });

let cookie = '';
async function call(method, path, body) {
  const res = await fetch(base + path, {
    method,
    headers: { 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const setCookie = res.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path} -> ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

async function waitForServer() {
  for (let i = 0; i < 50; i++) {
    try { await fetch(base + '/api/health'); return; } catch { await new Promise(r => setTimeout(r, 100)); }
  }
  throw new Error('Realmkeeper did not start:\n' + serverOutput);
}

function fakeMinecraftInstall(id) {
  const game = join(dataDir, 'servers', id, 'game');
  mkdirSync(join(game, 'world'), { recursive: true });
  writeFileSync(join(game, 'server.jar'), 'jar');
  writeFileSync(join(game, 'eula.txt'), 'eula=true\n');
  writeFileSync(join(game, 'world', 'level.dat'), 'level');
}

function fakeHytaleInstall(id) {
  const game = join(dataDir, 'servers', id, 'game');
  mkdirSync(join(game, 'Server'), { recursive: true });
  writeFileSync(join(game, 'Server', 'HytaleServer.jar'), '');
  writeFileSync(join(game, 'Assets.zip'), '');
  writeFileSync(join(game, 'start.sh'), [
    '#!/bin/bash',
    'echo "fake server ready"',
    'while read -r line; do',
    '  echo "received: $line"',
    // Real Hytale /who output: one line per world.
    '  if [ "$line" = "/who" ]; then echo "GrandMine (1): : Clarkonalark (Clarkonalark)"; fi',
    '  if [ "$line" = "/stop" ]; then echo "fake server stopping"; exit 0; fi',
    'done',
  ].join('\n'), { mode: 0o755 });
}

// A stand-in Terraria server: answers playing and exit, and prints the ": "
// prompt without a newline like the real console.
function fakeTerrariaInstall(id) {
  const game = join(dataDir, 'servers', id, 'game');
  mkdirSync(game, { recursive: true });
  writeFileSync(join(game, 'TerrariaServer.bin.x86_64'), [
    '#!/bin/bash',
    'echo "Server started"',
    'while true; do',
    '  printf ": "',
    '  read -r line || exit 0',
    '  case "$line" in',
    '    playing) echo "Bob (10.0.0.2:51234)"; echo "Ann (10.0.0.3:51235)"; echo "2 players connected." ;;',
    '    exit) echo "Saving world..."; exit 0 ;;',
    '    *) echo "received: $line" ;;',
    '  esac',
    'done',
  ].join('\n'), { mode: 0o755 });
}

const results = [];
async function check(name, fn) {
  try {
    await fn();
    results.push(['PASS', name]);
  } catch (err) {
    results.push(['FAIL', name, err.message.split('\n')[0]]);
  }
}

let browser;
try {
  await waitForServer();
  await call('POST', '/api/bootstrap', { username: 'admin', password: 'password123' });
  await call('POST', '/api/servers', { game: 'hytale', name: 'Alpha Hytale', memoryMin: '1G', memoryMax: '2G', backupFrequency: 30, maxPlayers: 20, gameMode: 'Adventure', password: 'join-secret-123' });
  await call('POST', '/api/servers', { game: 'minecraft-java', minecraftVariant: 'vanilla', minecraftVersion: 'latest', minecraftEulaAccepted: true, name: 'Bravo Minecraft', memoryMin: '1G', memoryMax: '2G', maxPlayers: 20, gameMode: 'Survival' });
  await call('POST', '/api/servers', { game: 'runescape-dragonwilds', name: 'Charlie Dragonwilds', maxPlayers: 6, dragonwildsOwnerId: 'owner-1', dragonwildsWorldName: 'World', dragonwildsAdminPassword: 'adminpass' });
  await call('POST', '/api/servers', { game: 'terraria', name: 'Delta Terraria', terrariaWorldName: 'Sky Island', terrariaWorldSize: 'large', gameMode: 'Expert' });
  fakeHytaleInstall('alpha-hytale');
  fakeTerrariaInstall('delta-terraria');
  fakeMinecraftInstall('bravo-minecraft');

  browser = await chromium.launch();
  const page = await browser.newPage();
  const pageErrors = [];
  // Server passwords must never be sent to the browser.
  const leakedSecrets = [];
  page.on('response', async res => {
    if (!res.url().includes('/api/')) return;
    const body = await res.text().catch(() => '');
    if (body.includes('join-secret-123')) leakedSecrets.push(new URL(res.url()).pathname);
  });
  page.on('pageerror', e => pageErrors.push(e.message));
  // The browser never reaches the real Modrinth; checks fake what they need.
  await page.route('**/api/modrinth/**', route => route.fulfill({ json: { hits: [], total: 0, offset: 0, gameVersion: '', loaders: [], updates: [], checked: 0, versions: [], pack: null } }));
  await page.route('**/api/modpacks/**', route => route.fulfill({ json: { hits: [], total: 0, offset: 0, versions: [] } }));
  // Requests that reach the backend without naming a server would be ambiguous.
  const unscoped = [];
  page.on('request', req => {
    const url = new URL(req.url());
    const serverScoped = /^\/api\/(status|players|resources|settings|server\/|console|logs|files|mods|backups|install\/(status|run-downloader|import-local|minecraft|dragonwilds|terraria)|auth|update|minecraft\/|modrinth\/|datapacks)/.test(url.pathname);
    if (serverScoped && !req.headers()['x-hsm-server']) unscoped.push(req.method() + ' ' + url.pathname);
  });

  const text = sel => page.textContent(sel);
  const value = sel => page.inputValue(sel);
  const visible = sel => page.isVisible(sel);
  const select = async id => { await page.click(`[data-select-server="${id}"]`); };
  const waitForName = name => page.waitForFunction(n => document.querySelector('#set-name').value === n, name, { timeout: 5000 });

  await page.goto(base);
  await page.fill('#auth-user', 'admin');
  await page.fill('#auth-pass', 'password123');
  await page.click('#auth-submit');
  await page.waitForSelector('#app:not(.hidden)');
  await page.waitForTimeout(500);

  await check('selecting a server loads its whole workspace', async () => {
    await select('bravo-minecraft');
    await waitForName('Bravo Minecraft');
    assert.equal(await text('#server-name'), 'Bravo Minecraft');
    assert.ok(await page.isVisible('#view-dashboard'), 'Overview section is shown');
    assert.ok(await page.isVisible('#server-head'), 'server header is shown');
    assert.notEqual(await text('#hero-state'), 'LOADING');
  });

  await check('switching games recalculates workspace tabs and controls', async () => {
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    assert.ok(await visible('[data-view-tab="mods"]'), 'Hytale shows Mods');
    assert.ok(await visible('[data-view-tab="backups"]'), 'Hytale shows Backups');
    assert.ok(!(await visible('[data-view-tab="players"]')), 'Hytale hides the Minecraft Players tab');
    assert.ok(await visible('.players-panel'), 'Hytale shows players');
    await select('bravo-minecraft');
    await waitForName('Bravo Minecraft');
    assert.ok(await visible('[data-view-tab="mods"]'), 'vanilla Minecraft shows the Mods tab for data packs');
    assert.equal(await text('#mods-tab-label'), 'Data packs');
    assert.ok(await visible('[data-view-tab="players"]'), 'Minecraft shows Players');
    assert.ok(await visible('[data-view-tab="backups"]'), 'Minecraft shows Backups (snapshots)');
    assert.ok(await visible('.players-panel'), 'Minecraft shows players');
    assert.match(await text('#game-capability-note'), /Minecraft Java servers do not support/);
    await select('charlie-dragonwilds');
    await waitForName('Charlie Dragonwilds');
    await page.click('[data-view-tab="console"]');
    assert.ok(!(await visible('#console-form')), 'Dragonwilds hides console command input');
    assert.ok(await visible('#console-output'), 'Dragonwilds still shows console output');
  });

  await check('only the active workspace section is shown', async () => {
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    for (const tab of ['config', 'console', 'settings']) {
      await page.click(`[data-view-tab="${tab}"]`);
      const shown = await page.$$eval('.view', views => views.filter(v => getComputedStyle(v).display !== 'none').map(v => v.id));
      assert.deepEqual(shown, ['view-' + tab]);
    }
    await page.click('[data-view-tab="dashboard"]');
  });

  await check('server list labels each game with its own protocol', async () => {
    assert.match(await text('[data-select-server="bravo-minecraft"]'), /TCP 2556\d/);
    assert.match(await text('[data-select-server="alpha-hytale"]'), /UDP 552\d/);
  });

  await check('a delayed response from the previous server is discarded', async () => {
    await select('bravo-minecraft');
    await waitForName('Bravo Minecraft');
    await page.route('**/api/settings', async route => {
      if (route.request().headers()['x-hsm-server'] === 'alpha-hytale') await new Promise(r => setTimeout(r, 1500));
      await route.continue().catch(() => {});
    });
    await select('alpha-hytale');
    await page.waitForTimeout(100);
    assert.equal(await value('#set-name'), '', 'settings form is cleared while loading, not showing the previous server');
    await select('bravo-minecraft');
    await page.waitForTimeout(2200);
    assert.equal(await value('#set-name'), 'Bravo Minecraft');
    assert.equal(await text('#server-name'), 'Bravo Minecraft');
    await page.unroute('**/api/settings');
  });

  await check('rapid switching settles on the last selected server', async () => {
    for (let i = 0; i < 8; i++) await select(i % 2 ? 'alpha-hytale' : 'bravo-minecraft');
    await select('charlie-dragonwilds');
    await waitForName('Charlie Dragonwilds');
    await page.waitForTimeout(800);
    assert.equal(await value('#set-name'), 'Charlie Dragonwilds');
    assert.equal(await text('#server-name'), 'Charlie Dragonwilds');
  });

  await check('an essential API failure shows an error with a working retry', async () => {
    let fail = true;
    await page.route('**/api/settings', route => fail && route.request().headers()['x-hsm-server'] === 'bravo-minecraft'
      ? route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"simulated failure"}' })
      : route.continue());
    await select('bravo-minecraft');
    await page.waitForSelector('#workspace-error:not(.hidden)', { timeout: 3000 });
    assert.match(await text('#workspace-error-text'), /simulated failure/);
    fail = false;
    await page.click('#workspace-retry');
    await waitForName('Bravo Minecraft');
    assert.ok(!(await visible('#workspace-error')), 'error clears after a successful retry');
    await page.unroute('**/api/settings');
  });

  await check('settings cannot be saved while the selected server is still loading', async () => {
    let release;
    const gate = new Promise(r => { release = r; });
    let saves = 0;
    await page.route('**/api/settings', async route => {
      if (route.request().method() === 'PUT') saves++;
      if (route.request().method() === 'GET' && route.request().headers()['x-hsm-server'] === 'alpha-hytale') await gate;
      await route.continue().catch(() => {});
    });
    await select('alpha-hytale');
    await page.click('[data-view-tab="settings"]');
    await page.fill('#set-name', 'Should not be saved');
    await page.click('#settings-form button[type="submit"], #settings-form button:not([type])');
    await page.waitForTimeout(300);
    assert.equal(saves, 0, 'no PUT was sent');
    release();
    await waitForName('Alpha Hytale');
    await page.unroute('**/api/settings');
    const servers = await call('GET', '/api/servers');
    assert.ok(servers.some(s => s.serverName === 'Alpha Hytale'), 'name unchanged on the backend');
  });

  await check('console output streams once and commands reach only the selected server', async () => {
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    await page.click('#start-btn');
    await page.waitForFunction(() => /Online|Starting/.test(document.querySelector('#top-status').textContent), null, { timeout: 5000 });
    await page.click('#players-refresh');
    await page.waitForFunction(() => document.querySelector('#players-list').textContent.includes('Clarkonalark'), null, { timeout: 5000 });
    assert.match(await text('#metric-players'), /^1 \/ 20$/, 'Hytale player count from /who');
    await page.click('[data-view-tab="console"]');
    await page.waitForFunction(() => document.querySelector('#console-output').textContent.includes('fake server ready'), null, { timeout: 5000 });
    await page.fill('#console-command', 'hello-alpha');
    await page.press('#console-command', 'Enter');
    await page.waitForFunction(() => document.querySelector('#console-output').textContent.includes('received: hello-alpha'), null, { timeout: 5000 });
    await page.waitForTimeout(2500); // let at least one poll cycle pass
    const count = await page.$$eval('#console-output .log-line', lines => lines.filter(l => l.textContent.includes('received: hello-alpha')).length);
    assert.equal(count, 1, 'streamed line appears exactly once');

    await select('bravo-minecraft');
    await waitForName('Bravo Minecraft');
    await page.click('[data-view-tab="console"]');
    assert.ok(!(await text('#console-output')).includes('hello-alpha'), "previous server's output is cleared");
    await page.fill('#console-command', 'meant-for-bravo');
    await page.press('#console-command', 'Enter');
    await page.waitForTimeout(500);
    const alphaLogs = await (await fetch(base + '/api/logs', { headers: { Cookie: cookie, 'X-HSM-Server': 'alpha-hytale' } })).json();
    assert.ok(!alphaLogs.some(l => l.line.includes('meant-for-bravo')), 'command was not delivered to the other server');
  });

  await check('a server stopping while open updates the workspace', async () => {
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    await fetch(base + '/api/servers/alpha-hytale/stop', { method: 'POST', headers: { Cookie: cookie } });
    await page.waitForFunction(() => /Stopped/.test(document.querySelector('#top-status').textContent), null, { timeout: 8000 });
    assert.ok(!(await page.isDisabled('#start-btn')), 'start is available again');
  });

  await check('Minecraft: players, live snapshot and game rules', async () => {
    await select('bravo-minecraft');
    await waitForName('Bravo Minecraft');
    await page.click('#start-btn');
    await page.waitForFunction(() => /Online|Starting/.test(document.querySelector('#top-status').textContent), null, { timeout: 8000 });
    await page.click('#players-refresh');
    await page.waitForFunction(() => document.querySelector('#players-list').textContent.includes('Steve'), null, { timeout: 5000 });
    assert.match(await text('#metric-players'), /2 \/ 20/);

    await page.click('[data-view-tab="backups"]');
    await page.waitForTimeout(600);
    assert.ok(!(await page.evaluate(() => document.querySelector('#toast').classList.contains('error') && document.querySelector('#toast').classList.contains('show'))), 'Backups tab loads without an error: ' + await text('#toast'));
    assert.ok(!(await visible('#native-backups-card')), 'Hytale native backups are hidden for Minecraft');
    assert.ok(!(await visible('#backup-frequency')), 'Hytale backup interval is hidden for Minecraft');
    await page.click('#snapshot-backup');
    await page.waitForFunction(() => /snapshot-/.test(document.querySelector('#snapshots').textContent), null, { timeout: 8000 });

    await page.click('[data-view-tab="settings"]');
    await page.waitForSelector('#mcp-difficulty', { timeout: 5000 });
    await page.selectOption('#mcp-difficulty', 'hard');
    await page.click('#mc-properties-save');
    await page.waitForTimeout(500);
    const props = await (await fetch(base + '/api/files/content?path=' + encodeURIComponent('game/server.properties'), { headers: { Cookie: cookie, 'X-HSM-Server': 'bravo-minecraft' } })).json();
    assert.match(props.content, /^difficulty=hard$/m);
    await fetch(base + '/api/servers/bravo-minecraft/stop', { method: 'POST', headers: { Cookie: cookie } });
    await page.waitForFunction(() => /Stopped/.test(document.querySelector('#top-status').textContent), null, { timeout: 8000 });
  });

  await check('Minecraft players: operators from the console, bans while stopped', async () => {
    await select('bravo-minecraft');
    await waitForName('Bravo Minecraft');
    await page.click('#start-btn');
    await page.waitForFunction(() => /Online|Starting/.test(document.querySelector('#top-status').textContent), null, { timeout: 8000 });
    await page.click('[data-view-tab="players"]');
    await page.waitForFunction(() => document.querySelector('#pm-online').textContent.includes('Steve'), null, { timeout: 5000 });
    assert.match(await text('#pm-mode-text'), /running/);
    await page.click('#pm-online [data-pm-action="op"][data-pm-name="Steve"]');
    await page.waitForFunction(() => document.querySelector('#pm-ops').textContent.includes('Steve'), null, { timeout: 6000 });
    assert.ok(await visible('#pm-online [data-pm-action="deop"][data-pm-name="Steve"]'), 'Steve is shown as an operator');
    await fetch(base + '/api/servers/bravo-minecraft/stop', { method: 'POST', headers: { Cookie: cookie } });
    await page.waitForFunction(() => /Stopped/.test(document.querySelector('#top-status').textContent), null, { timeout: 8000 });

    await page.click('[data-view-tab="dashboard"]');
    await page.click('[data-view-tab="players"]');
    await page.waitForFunction(() => /stopped/.test(document.querySelector('#pm-mode-text').textContent), null, { timeout: 5000 });
    await page.fill('#pm-ip', '203.0.113.9');
    page.once('dialog', d => d.accept('spam'));
    await page.click('#pm-ban-ip');
    await page.waitForFunction(() => document.querySelector('#pm-bans').textContent.includes('203.0.113.9'), null, { timeout: 5000 });
    assert.match(await text('#pm-bans'), /spam/);
    const bans = await (await fetch(base + '/api/files/content?path=' + encodeURIComponent('game/banned-ips.json'), { headers: { Cookie: cookie, 'X-HSM-Server': 'bravo-minecraft' } })).json();
    assert.match(bans.content, /"ip": "203\.0\.113\.9"/);
    await page.click('#pm-bans [data-pm-action="pardon-ip"]');
    await page.waitForFunction(() => !document.querySelector('#pm-bans').textContent.includes('203.0.113.9'), null, { timeout: 5000 });
  });

  await check('Terraria: fixed world settings, players and install state', async () => {
    await select('delta-terraria');
    await waitForName('Delta Terraria');
    await page.click('[data-view-tab="settings"]');
    assert.equal(await text('#set-mode-label'), 'Difficulty');
    assert.equal(await value('#set-mode'), 'Expert');
    assert.ok(await page.isDisabled('#set-mode'), 'difficulty is fixed after the world is created');
    assert.ok(!(await visible('#set-xms-row')) && !(await visible('#set-whitelist-row')), 'JVM memory and whitelist do not apply to Terraria');
    assert.match(await text('#terraria-settings-note'), /Sky Island/);
    assert.equal(await page.getAttribute('#set-players', 'max'), '255');

    await page.click('[data-view-tab="dashboard"]');
    await page.click('#start-btn');
    await page.waitForFunction(() => /Online|Starting/.test(document.querySelector('#top-status').textContent), null, { timeout: 8000 });
    await page.click('#players-refresh');
    await page.waitForFunction(() => document.querySelector('#players-list').textContent.includes('Bob'), null, { timeout: 5000 });
    assert.match(await text('#metric-players'), /2 \/ 8/);
    const cfg = await (await fetch(base + '/api/files/content?path=' + encodeURIComponent('game/serverconfig.txt'), { headers: { Cookie: cookie, 'X-HSM-Server': 'delta-terraria' } })).json();
    assert.match(cfg.content, /^worldname=Sky Island$/m);
    assert.match(cfg.content, /^difficulty=1$/m);
    await fetch(base + '/api/servers/delta-terraria/stop', { method: 'POST', headers: { Cookie: cookie } });
    await page.waitForFunction(() => /Stopped/.test(document.querySelector('#top-status').textContent), null, { timeout: 8000 });

    await page.click('[data-view-tab="setup"]');
    await page.waitForSelector('#terraria-install-controls:not(.hidden)', { timeout: 5000 });
    assert.ok(!(await visible('#hytale-install-controls')), 'Hytale installer is hidden for Terraria');
    assert.match(await text('#install-state'), /Terraria server installed/);
  });

  await check('Paper servers manage plugins in the Mods tab', async () => {
    await call('POST', '/api/servers', { game: 'minecraft-java', minecraftVariant: 'paper', minecraftVersion: 'latest', minecraftEulaAccepted: true, name: 'Echo Paper', memoryMin: '1G', memoryMax: '2G' });
    try {
      await page.waitForSelector('[data-select-server="echo-paper"]', { timeout: 10000 });
      await select('echo-paper');
      await waitForName('Echo Paper');
      assert.equal(await text('#mods-tab-label'), 'Plugins');
      await page.click('[data-view-tab="mods"]');
      assert.equal(await text('#mods-title'), 'Plugins');
      await page.setInputFiles('#mod-file', { name: 'EssentialsX.jar', mimeType: 'application/java-archive', buffer: Buffer.from('jar') });
      await page.waitForFunction(() => document.querySelector('#mods-list').textContent.includes('EssentialsX.jar'), null, { timeout: 5000 });
      await page.click('[data-view-tab="setup"]');
      await page.waitForFunction(() => /Paper/.test(document.querySelector('#minecraft-install').textContent), null, { timeout: 5000 });
      await select('bravo-minecraft');
      await waitForName('Bravo Minecraft');
      assert.equal(await text('#mods-tab-label'), 'Data packs', 'vanilla Minecraft has data packs, not mods');
    } finally {
      await call('DELETE', '/api/servers/echo-paper').catch(() => {});
    }
  });

  await check('NeoForge servers manage mods and install with the loader', async () => {
    await call('POST', '/api/servers', { game: 'minecraft-java', minecraftVariant: 'neoforge', minecraftVersion: 'latest', minecraftEulaAccepted: true, name: 'Golf NeoForge', memoryMin: '1G', memoryMax: '2G' });
    try {
      await page.waitForSelector('[data-select-server="golf-neoforge"]', { timeout: 10000 });
      await select('golf-neoforge');
      await waitForName('Golf NeoForge');
      assert.equal(await text('#mods-tab-label'), 'Mods');
      await page.click('[data-view-tab="setup"]');
      await page.waitForFunction(() => document.querySelector('#minecraft-install').textContent === 'Install NeoForge server', null, { timeout: 5000 });
      assert.match(await text('#minecraft-install-help'), /NeoForge/);

      // The Modrinth browser, with the app's Modrinth endpoints faked.
      let installBody = null;
      const isSearch = url => url.pathname === '/api/modrinth/search';
      await page.route(isSearch, route => route.fulfill({ json: { hits: [{ project_id: 'jei1', slug: 'jei', title: 'Just Enough Items', author: 'mezz', downloads: 123456, description: 'Item viewer', icon_url: '', installed: false }], total: 1, offset: 0, gameVersion: '', loaders: ['neoforge'] } }));
      await page.route('**/api/modrinth/projects/jei1/versions*', route => route.fulfill({ json: { versions: [{ id: 'v19', versionNumber: '19.0', versionType: 'release', published: '2026-09-01T00:00:00Z' }, { id: 'v20', versionNumber: '20.0', versionType: 'beta', published: '2026-09-20T00:00:00Z' }] } }));
      await page.route('**/api/modrinth/install', route => { installBody = route.request().postDataJSON(); route.fulfill({ json: { installed: [{ projectId: 'jei1', title: 'Just Enough Items', versionNumber: '19.0' }], message: 'Installed Just Enough Items 19.0' } }); });
      try {
        await page.click('[data-view-tab="mods"]');
        await page.waitForFunction(() => document.querySelector('#modrinth-results').textContent.includes('Just Enough Items'), null, { timeout: 5000 });
        assert.ok(await visible('#modrinth-card'), 'the Modrinth browser is shown for NeoForge');
        assert.match(await text('#modrinth-help'), /neoforge/);
        assert.match(await text('#modrinth-results'), /123k downloads/);
        await page.click('#modrinth-results [data-mr-versions]');
        await page.waitForSelector('#modrinth-results select');
        await page.selectOption('#modrinth-results select', 'v19');
        await page.click('#modrinth-results [data-mr-install]');
        await page.waitForFunction(() => document.querySelector('#modrinth-results').textContent.includes('Installed'), null, { timeout: 5000 });
        assert.deepEqual(installBody, { kind: 'mod', projectId: 'jei1', versionId: 'v19' });
        assert.deepEqual(await page.$$eval('#modrinth-kind option', o => o.map(x => x.value)), ['mod', 'datapack', 'modpack']);
        assert.ok(await visible('#datapacks-card') && await visible('#modpack-card'), 'NeoForge shows data packs and the mod pack card');
      } finally {
        await page.unroute(isSearch);
        await page.unroute('**/api/modrinth/projects/jei1/versions*');
        await page.unroute('**/api/modrinth/install');
      }
    } finally {
      await call('DELETE', '/api/servers/golf-neoforge').catch(() => {});
    }
  });

  await check('data packs on vanilla, and mod packs installed and updated on Fabric', async () => {
    const searches = [];
    const isSearch = url => url.pathname === '/api/modrinth/search';
    await page.route(isSearch, route => {
      const kind = new URL(route.request().url()).searchParams.get('kind');
      searches.push(kind);
      const hit = kind === 'modpack' ? { project_id: 'pack1', title: 'Sky Pack' } : { project_id: 'dp1', title: 'Terralith' };
      route.fulfill({ json: { hits: [{ ...hit, slug: 'x', author: 'a', downloads: 10, description: '', icon_url: '', installed: false }], total: 1, offset: 0, gameVersion: '1.21.8', loaders: [kind === 'datapack' ? 'datapack' : 'fabric'] } });
    });
    let installBody = null, packBody = null;
    await page.route('**/api/modrinth/install', route => { installBody = route.request().postDataJSON(); route.fulfill({ json: { installed: [{ projectId: 'dp1', title: 'Terralith', versionNumber: '2.0' }], message: 'Installed Terralith 2.0' } }); });
    await page.route('**/api/modrinth/modpack', route => {
      if (route.request().method() === 'POST') { packBody = route.request().postDataJSON(); return route.fulfill({ status: 202, json: { ok: true, message: 'Mod pack installation started' } }); }
      route.fulfill({ json: { busy: false, pack: { projectId: 'pack1', versionId: 'p1', versionNumber: '1.0', title: 'Sky Pack', loader: 'fabric', loaderVersion: '0.17.2', minecraftVersion: '1.21.8', files: ['mods/a.jar'] } } });
    });
    await page.route('**/api/modrinth/projects/pack1/versions*', route => route.fulfill({ json: { versions: [{ id: 'p2', versionNumber: '2.0', versionType: 'release', published: '2026-10-01T00:00:00Z' }, { id: 'p1', versionNumber: '1.0', versionType: 'release', published: '2026-09-01T00:00:00Z' }] } }));
    await call('POST', '/api/servers', { game: 'minecraft-java', minecraftVariant: 'vanilla', minecraftVersion: 'latest', minecraftEulaAccepted: true, name: 'Hotel Vanilla', memoryMin: '1G', memoryMax: '2G' });
    await call('POST', '/api/servers', { game: 'minecraft-java', minecraftVariant: 'fabric', minecraftVersion: 'latest', minecraftEulaAccepted: true, name: 'India Fabric', memoryMin: '1G', memoryMax: '2G' });
    try {
      await page.waitForSelector('[data-select-server="hotel-vanilla"]', { timeout: 10000 });
      await select('hotel-vanilla');
      await waitForName('Hotel Vanilla');
      await page.click('[data-view-tab="mods"]');
      await page.waitForFunction(() => document.querySelector('#modrinth-results').textContent.includes('Terralith'), null, { timeout: 5000 });
      assert.ok(!(await visible('#mods-card')) && !(await visible('#modpack-card')), 'vanilla has no mods or mod packs');
      assert.ok(await visible('#datapacks-card'), 'vanilla manages data packs');
      assert.ok(!(await visible('#modrinth-kind')), 'one kind to browse needs no picker');
      await page.waitForFunction(() => /world\/datapacks/.test(document.querySelector('#datapacks-help').textContent), null, { timeout: 5000 });
      assert.equal(searches.at(-1), 'datapack');
      await page.click('#modrinth-results [data-mr-install]');
      await page.waitForFunction(() => document.querySelector('#modrinth-results').textContent.includes('Installed'), null, { timeout: 5000 });
      assert.deepEqual(installBody, { kind: 'datapack', projectId: 'dp1', versionId: '' });

      await select('india-fabric');
      await waitForName('India Fabric');
      await page.click('[data-view-tab="mods"]');
      await page.waitForFunction(() => document.querySelector('#modpack-current').textContent.includes('Sky Pack'), null, { timeout: 5000 });
      assert.match(await text('#modpack-current'), /Version 1\.0 · fabric 0\.17\.2 · Minecraft 1\.21\.8/);
      await page.click('#modpack-check');
      await page.waitForSelector('[data-pack-update="p2"]', { timeout: 5000 });
      page.once('dialog', d => d.accept());
      await page.click('[data-pack-update="p2"]');
      await page.waitForFunction(() => document.querySelector('#toast').textContent.includes('Mod pack installation started'), null, { timeout: 5000 });
      assert.deepEqual(packBody, { projectId: 'pack1', versionId: 'p2' });
      await page.selectOption('#modrinth-kind', 'modpack');
      await page.waitForFunction(() => document.querySelector('#modrinth-results').textContent.includes('Sky Pack'), null, { timeout: 5000 });
      assert.equal(searches.at(-1), 'modpack');
      assert.match(await text('#modrinth-results [data-mr-install]'), /pack/);
    } finally {
      await page.unroute(isSearch);
      await page.unroute('**/api/modrinth/install');
      await page.unroute('**/api/modrinth/modpack');
      await page.unroute('**/api/modrinth/projects/pack1/versions*');
      await call('DELETE', '/api/servers/hotel-vanilla').catch(() => {});
      await call('DELETE', '/api/servers/india-fabric').catch(() => {});
    }
  });

  await check('a server can be created from a Modrinth mod pack', async () => {
    let created = null;
    await page.route('**/api/modpacks/search*', route => route.fulfill({ json: { hits: [{ project_id: 'pack1', title: 'Sky Pack', author: 'a', downloads: 5000, description: 'Skyblock', icon_url: '' }], total: 1, offset: 0 } }));
    await page.route('**/api/modpacks/pack1/versions', route => route.fulfill({ json: { versions: [{ id: 'p2', versionNumber: '2.0', versionType: 'release', gameVersions: ['1.21.1'], loaders: ['neoforge'] }] } }));
    await page.route('**/api/servers', async route => {
      if (route.request().method() !== 'POST') return route.fallback();
      created = route.request().postDataJSON();
      route.fulfill({ status: 400, json: { error: 'stop here' } });
    });
    try {
      await page.click('#add-server-side');
      await page.click('[data-game-choice="minecraft-java"]');
      await page.click('#wizard-next');
      await page.click('#wizard-next');
      await page.check('#new-minecraft-eula');
      await page.selectOption('#new-minecraft-variant', 'modpack');
      assert.ok(!(await visible('#new-minecraft-version-row')), 'the pack decides the version');
      await page.waitForSelector('#new-modpack-results [data-use-pack]', { timeout: 5000 });
      await page.click('#wizard-next');
      assert.match(await text('#toast'), /Choose a mod pack/);
      await page.click('#new-modpack-results [data-use-pack]');
      await page.waitForFunction(() => !document.querySelector('#new-modpack-version').disabled, null, { timeout: 5000 });
      assert.match(await text('#new-modpack-version'), /2\.0 · Minecraft 1\.21\.1 · neoforge/);
      while (await visible('#wizard-next')) await page.click('#wizard-next');
      assert.match(await text('#wizard-review'), /Sky Pack/);
      await page.click('#wizard-create');
      await page.waitForFunction(() => document.querySelector('#toast').textContent.includes('stop here'), null, { timeout: 5000 });
      assert.deepEqual(created.minecraftModpack, { projectId: 'pack1', versionId: 'p2' });
      assert.equal(created.minecraftVariant, '');
      await page.click('#cancel-server-dialog');
    } finally {
      await page.unroute('**/api/modpacks/search*');
      await page.unroute('**/api/modpacks/pack1/versions');
      await page.unroute('**/api/servers');
    }
  });

  await check('tModLoader servers take .tmod mods', async () => {
    await call('POST', '/api/servers', { game: 'terraria', terrariaVariant: 'tmodloader', name: 'Foxtrot Modded' });
    try {
      await page.waitForSelector('[data-select-server="foxtrot-modded"]', { timeout: 10000 });
      await select('foxtrot-modded');
      await waitForName('Foxtrot Modded');
      assert.equal(await text('#mods-tab-label'), 'Mods');
      await page.click('[data-view-tab="mods"]');
      assert.equal(await page.getAttribute('#mod-file', 'accept'), '.tmod');
      await page.setInputFiles('#mod-file', { name: 'CalamityMod.tmod', mimeType: 'application/octet-stream', buffer: Buffer.from('tmod') });
      await page.waitForFunction(() => document.querySelector('#mods-list').textContent.includes('CalamityMod.tmod'), null, { timeout: 5000 });
      const enabled = await (await fetch(base + '/api/files/content?path=' + encodeURIComponent('game/Mods/enabled.json'), { headers: { Cookie: cookie, 'X-HSM-Server': 'foxtrot-modded' } })).json();
      assert.deepEqual(JSON.parse(enabled.content), ['CalamityMod']);
      await page.click('[data-view-tab="setup"]');
      await page.waitForFunction(() => /tModLoader/.test(document.querySelector('#terraria-install').textContent), null, { timeout: 5000 });
    } finally {
      await call('DELETE', '/api/servers/foxtrot-modded').catch(() => {});
    }
  });

  await check('Dragonwilds worlds can be uploaded, switched and started fresh', async () => {
    const saves = join(dataDir, 'servers', 'charlie-dragonwilds', 'game', 'RSDragonwilds', 'Saved', 'SaveGames');
    mkdirSync(saves, { recursive: true });
    writeFileSync(join(saves, 'Old Vale.sav'), 'old');
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    assert.ok(!(await visible('[data-view-tab="worlds"]')), 'Hytale has no Worlds tab');
    await select('charlie-dragonwilds');
    await waitForName('Charlie Dragonwilds');
    await page.click('[data-view-tab="worlds"]');
    await page.waitForFunction(() => document.querySelector('#world-list').textContent.includes('Old Vale'), null, { timeout: 5000 });
    await page.setInputFiles('#world-file', [
      { name: 'Misty Peaks.sav', mimeType: 'application/octet-stream', buffer: Buffer.from('sav') },
      { name: 'Misty Peaks.backup', mimeType: 'application/octet-stream', buffer: Buffer.from('bak') },
    ]);
    await page.waitForFunction(() => /Misty Peaks.*Loads next/.test(document.querySelector('#world-list').textContent), null, { timeout: 5000 });
    assert.match(await text('#world-list'), /has \.backup/);
    await page.click('[data-world-action="use"][data-world="Old Vale"]');
    await page.waitForFunction(() => /Old Vale\s*Loads next/.test(document.querySelector('#world-list').textContent), null, { timeout: 5000 });
    await page.fill('#new-world-name', 'Fresh Start');
    page.once('dialog', d => d.accept());
    await page.click('#new-world-btn');
    await page.waitForFunction(() => /No saved worlds yet.*Fresh Start/.test(document.querySelector('#world-list').textContent), null, { timeout: 5000 });
    assert.ok(await visible('#world-archive-card'), 'moved saves are listed as world backups');
    const settings = await (await fetch(base + '/api/settings', { headers: { Cookie: cookie, 'X-HSM-Server': 'charlie-dragonwilds' } })).json();
    assert.equal(settings.dragonwildsWorldName, 'Fresh Start');
  });

  await check('a server can be exported and imported as a copy', async () => {
    await select('charlie-dragonwilds');
    await waitForName('Charlie Dragonwilds');
    await page.click('[data-view-tab="settings"]');
    const [download] = await Promise.all([page.waitForEvent('download', { timeout: 10000 }), page.click('#export-server')]);
    assert.match(download.suggestedFilename(), /^realmkeeper-charlie-dragonwilds-.*\.tar\.gz$/);
    const file = await download.path();
    await page.click('#back-to-servers').catch(() => page.click('[data-view="servers"]'));
    await page.waitForSelector('[data-import-server]:visible', { timeout: 5000 });
    await page.setInputFiles('#import-file', file);
    await page.waitForFunction(() => document.querySelectorAll('[data-select-server^="charlie-dragonwilds-"]').length === 1, null, { timeout: 10000 });
    const servers = await call('GET', '/api/servers');
    const copy = servers.find(s => s.id.startsWith('charlie-dragonwilds-'));
    assert.ok(copy && copy.port !== servers.find(s => s.id === 'charlie-dragonwilds').port, 'the copy gets its own port');
    await call('DELETE', '/api/servers/' + copy.id);
  });

  await check('a stopped Hytale server can switch to the pre-release channel', async () => {
    await fetch(base + '/api/servers/alpha-hytale/stop', { method: 'POST', headers: { Cookie: cookie } }).catch(() => {});
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    await page.click('[data-view-tab="dashboard"]');
    await page.waitForFunction(() => /Stopped|Offline/.test(document.querySelector('#top-status').textContent), null, { timeout: 8000 });
    assert.ok(await page.isDisabled('#hytale-patchline-switch'), 'nothing to switch yet');
    await page.selectOption('#hytale-patchline', 'pre-release');
    page.once('dialog', d => d.accept());
    await page.click('#hytale-patchline-switch');
    await page.waitForFunction(() => document.querySelector('#hytale-patchline-switch').disabled, null, { timeout: 5000 });
    const settings = await (await fetch(base + '/api/settings', { headers: { Cookie: cookie, 'X-HSM-Server': 'alpha-hytale' } })).json();
    assert.equal(settings.hytalePatchline, 'pre-release');
    const cfg = await (await fetch(base + '/api/files/content?path=' + encodeURIComponent('game/Server/config.json'), { headers: { Cookie: cookie, 'X-HSM-Server': 'alpha-hytale' } })).json();
    assert.match(cfg.content, /"Patchline": "pre-release"/);
    const backups = await (await fetch(base + '/api/backups', { headers: { Cookie: cookie, 'X-HSM-Server': 'alpha-hytale' } })).json();
    assert.ok(backups.snapshots.length > 0, 'a snapshot was taken before switching');
  });

  await check('the wizard applies each game\'s rules', async () => {
    await page.click('#add-server-side');
    await page.click('[data-game-choice="terraria"]');
    await page.click('#wizard-next');
    await page.click('#wizard-next');
    assert.ok(await visible('#terraria-fields'), 'Terraria world fields are shown');
    assert.equal(await text('#new-mode-label'), 'Difficulty');
    assert.deepEqual(await page.$$eval('#new-server-mode option', o => o.map(x => x.value)), ['Classic', 'Expert', 'Master', 'Journey']);
    assert.equal(await value('#new-server-players'), '8');
    await page.selectOption('#new-terraria-variant', 'tmodloader');
    assert.match(await text('#new-terraria-variant-help'), /\.NET/);
    assert.equal(await page.getAttribute('#new-terraria-version', 'placeholder'), 'latest or v2025.08.3.0');
    assert.ok(!(await visible('#new-whitelist-row')), 'no whitelist for Terraria');
    assert.ok(!(await visible('#new-patchline-row')), 'no Hytale release channel for Terraria');
    await page.click('#wizard-next');
    assert.ok(!(await visible('#new-xms-row')) && !(await visible('#new-backup-row')), 'no JVM memory or Hytale backups for Terraria');
    await page.click('#wizard-back');
    await page.click('#wizard-back');
    await page.click('#wizard-back');
    await page.click('[data-game-choice="minecraft-java"]');
    await page.click('#wizard-next');
    await page.click('#wizard-next');
    assert.equal(await value('#new-server-mode'), 'Survival');
    assert.deepEqual(await page.$$eval('#new-minecraft-variant option', o => o.map(x => x.value)), ['vanilla', 'paper', 'fabric', 'quilt', 'forge', 'neoforge', 'modpack']);
    await page.click('#wizard-back');
    await page.click('#wizard-back');
    await page.click('[data-game-choice="runescape-dragonwilds"]');
    await page.click('#wizard-next');
    await page.click('#wizard-next');
    assert.equal(await value('#new-dragonwilds-owner'), 'owner-1', 'the Owner ID is remembered from the existing Dragonwilds server');
    await page.click('#wizard-back');
    await page.click('#wizard-back');
    await page.click('[data-game-choice="minecraft-java"]');
    await page.click('#wizard-next');
    await page.click('#wizard-next');
    await page.selectOption('#new-minecraft-variant', 'fabric');
    assert.match(await text('#new-minecraft-variant-help'), /Fabric/);
    for (const loader of ['Quilt', 'Forge', 'NeoForge']) {
      await page.selectOption('#new-minecraft-variant', loader.toLowerCase());
      assert.match(await text('#new-minecraft-variant-help'), new RegExp(loader));
    }
    assert.ok(!(await visible('#terraria-fields')), 'Terraria fields are hidden for Minecraft');
    await page.click('#cancel-server-dialog');
  });

  await check('a selected server deleted elsewhere moves the workspace to a valid server', async () => {
    await select('bravo-minecraft');
    await waitForName('Bravo Minecraft');
    await call('DELETE', '/api/servers/bravo-minecraft');
    await page.waitForFunction(() => !document.querySelector('[data-select-server="bravo-minecraft"]'), null, { timeout: 5000 });
    await page.waitForFunction(() => document.querySelector('#set-name').value && document.querySelector('#set-name').value !== 'Bravo Minecraft', null, { timeout: 5000 });
    assert.notEqual(await text('#server-name'), 'Bravo Minecraft');
  });

  await check('the create-server wizard opens', async () => {
    await page.click('#add-server-side');
    assert.equal(await page.evaluate(() => document.querySelector('#server-dialog').open), true);
    await page.click('#cancel-server-dialog');
  });

  await check('an unpublished game port shows a warning only for that server', async () => {
    await page.route('**/api/servers', async route => {
      const res = await route.fetch();
      const servers = await res.json();
      for (const s of servers) s.portPublished = s.id !== 'charlie-dragonwilds';
      await route.fulfill({ response: res, json: servers });
    });
    try {
      await select('charlie-dragonwilds');
      await waitForName('Charlie Dragonwilds');
      await page.click('[data-view-tab="dashboard"]');
      await page.waitForSelector('#port-warning:not(.hidden)', { timeout: 5000 });
      assert.match(await text('#port-warning'), /\/UDP is not published/);
      await select('alpha-hytale');
      await waitForName('Alpha Hytale');
      await page.waitForTimeout(300);
      assert.equal(await visible('#port-warning'), false, 'a published port shows no warning');
    } finally {
      await page.unroute('**/api/servers');
    }
  });

  await check('every sidebar server can be clicked on a short window', async () => {
    await page.setViewportSize({ width: 1280, height: 520 });
    try {
      for (const id of await page.$$eval('[data-select-server]', els => els.map(e => e.dataset.selectServer))) {
        await page.click(`[data-select-server="${id}"]`, { timeout: 3000 });
      }
    } finally {
      await page.setViewportSize({ width: 1280, height: 720 });
    }
  });

  await check('mobile overview has no horizontal page overflow', async () => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(400); // drawer hides after its slide-out transition
    assert.ok(!(await visible('[data-select-server="alpha-hytale"]')), 'server list is in the closed drawer');
    await page.click('#nav-toggle');
    await page.waitForTimeout(300);
    await select('alpha-hytale');
    await page.waitForTimeout(300);
    assert.ok(!(await page.evaluate(() => document.querySelector('#app').classList.contains('nav-open'))), 'drawer closes after choosing a server');
    await page.waitForTimeout(500);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    assert.ok(overflow <= 0, `page overflows by ${overflow}px`);
  });

  await check('saved passwords are not sent to the browser and are kept on save', async () => {
    await page.setViewportSize({ width: 1366, height: 860 });
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    await page.click('[data-view-tab="settings"]');
    assert.equal(await value('#set-password'), '', 'password field starts blank');
    assert.match(await page.getAttribute('#set-password', 'placeholder'), /Password set/);
    assert.ok(await visible('#set-password-clear-row'), 'remove-password option is offered');
    await page.click('#settings-form button:not([type])');
    await page.waitForTimeout(600);
    // Saving with a blank password field keeps the existing password; the
    // server's own config.json (written on save while stopped) shows it.
    const file = await (await fetch(base + '/api/server/file/config.json', { headers: { Cookie: cookie, 'X-HSM-Server': 'alpha-hytale' } })).json();
    assert.equal(JSON.parse(file.content).Password, 'join-secret-123', 'password kept after saving with a blank field');
    assert.deepEqual(leakedSecrets, []);
  });

  await check('schedules can be created, listed, disabled and survive a reload', async () => {
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    await page.click('[data-view-tab="schedules"]');
    await page.waitForFunction(() => /No schedules yet/.test(document.querySelector('#schedule-list').textContent), null, { timeout: 5000 });
    await page.click('#schedule-add');
    await page.fill('#sch-name', 'Nightly restart');
    await page.selectOption('#sch-action', 'restart');
    await page.selectOption('#sch-kind', 'daily');
    await page.fill('#sch-time', '04:30');
    await page.check('#sch-days-row input[value="1"]');
    assert.ok(await visible('#sch-warn-row'), 'Hytale restart offers player warnings');
    await page.click('#sch-save');
    await page.waitForFunction(() => /Nightly restart/.test(document.querySelector('#schedule-list').textContent), null, { timeout: 5000 });
    assert.match(await text('#schedule-list'), /Mon at 04:30/);
    assert.match(await text('#schedule-list'), /Next: /);
    await page.uncheck('[data-schedule-toggle]');
    await page.waitForFunction(() => document.querySelector('.schedule-row')?.classList.contains('disabled'), null, { timeout: 5000 });
    await page.reload();
    await page.waitForSelector('#app:not(.hidden)');
    await page.waitForTimeout(600);
    await select('bravo-minecraft').catch(() => {});
    await select('alpha-hytale');
    await waitForName('Alpha Hytale');
    await page.click('[data-view-tab="schedules"]');
    await page.waitForFunction(() => /Nightly restart/.test(document.querySelector('#schedule-list').textContent), null, { timeout: 5000 });
    assert.match(await text('#schedule-list'), /Disabled/);
  });

  await check('auto-restart on crash is saved with the server settings', async () => {
    await page.click('[data-view-tab="settings"]');
    await page.check('#set-autorestart');
    await page.click('#settings-form button:not([type])');
    await page.waitForTimeout(600);
    const settings = await (await fetch(base + '/api/settings', { headers: { Cookie: cookie, 'X-HSM-Server': 'alpha-hytale' } })).json();
    assert.equal(settings.autoRestart, true);
  });

  await check('no uncaught page errors', async () => { assert.deepEqual(pageErrors, []); });
  await check('every server-scoped request names its server', async () => { assert.deepEqual([...new Set(unscoped)], []); });
} finally {
  await browser?.close();
  server.kill('SIGTERM');
  rmSync(dataDir, { recursive: true, force: true });
  rmSync(fakeBin, { recursive: true, force: true });
}

for (const [status, name, detail] of results) console.log(`${status}  ${name}${detail ? ' — ' + detail : ''}`);
const failed = results.filter(r => r[0] === 'FAIL').length;
console.log(`\n${results.length - failed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
