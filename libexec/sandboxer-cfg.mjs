#!/usr/bin/env node
// sandboxer-cfg — конфиг-резолвер песочницы (Nix-профиль) + перенос зависимостей.
//
// Команды:
//   eval <profile.nix>
//       вычисляет Nix-профиль (`nix eval --impure --json`) и печатает JSON в stdout.
//       Профиль — это либо attrset, либо функция (тогда зовётся как `f {}`).
//   in  <profileJson> <sandboxDir> <manifestOut> [--force]
//       копирует `srcs` (origins) ВНУТРЬ песочницы, пишет манифест.
//       По умолчанию НЕ затирает локально изменённые цели (как depsync); --force затирает.
//   out <manifestFile> [--force]
//       копирует rw-записи из песочницы ОБРАТНО поверх origins.
//       По умолчанию НЕ затирает origins, изменённые после pull; --force затирает.
//
// `srcs` — список записей одного из двух видов:
//   ЯВНЫЙ:   { from = "/abs/dir|file"; to = "vendor/x"; mode = "rw"|"ro"; }
//            (копирует from -> <sandbox>/<to>; to по умолчанию = basename(from))
//   МАТЧЕР:  { root = "/abs|rel"; name|glob|regex = "..."; to = "."; mode; depth; }
//            (как depsync: ищет под root, копирует совпадения в <sandbox>/<to>/<rel>)

import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

const SKIP = new Set(['.git', 'node_modules', '.sandboxer']);

// ---- Nix-профиль -> JSON --------------------------------------------------
function evalProfile(file) {
  const abs = path.resolve(file);
  const out = execFileSync('nix', [
    '--extra-experimental-features', 'nix-command',
    'eval', '--impure', '--json', '--no-allow-import-from-derivation', '--expr',
    `let f = import ${JSON.stringify(abs)}; in if builtins.isFunction f then f {} else f`,
  ], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  return JSON.parse(out);
}

// ---- матчер (ant-glob, как в depsync/прежнем cfg) -------------------------
function globToRe(glob) {
  let re = '';
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i];
    if (c === '*') {
      if (glob[i + 1] === '*') { re += '.*'; i++; if (glob[i + 1] === '/') i++; }
      else re += '[^/]*';
    } else if (c === '?') re += '[^/]';
    else if ('.+^${}()|[]\\'.includes(c)) re += '\\' + c;
    else re += c;
  }
  return new RegExp('^' + re + '$');
}

function* walk(root, depth, cur = 0) {
  let ents;
  try { ents = fs.readdirSync(root, { withFileTypes: true }); } catch { return; }
  for (const e of ents) {
    if (SKIP.has(e.name)) continue;
    const p = path.join(root, e.name);
    yield { p, dirent: e };
    if (e.isDirectory() && (!depth || cur + 1 < depth)) yield* walk(p, depth, cur + 1);
  }
}

function matchEntries(m, defaultRoot) {
  const root = path.resolve(defaultRoot, m.root || '.');
  const to = m.to || '.';
  const mode = (m.mode || 'rw').toLowerCase();
  const depth = Number(m.depth || 0);
  const results = [];
  let test;
  if (m.name) { const re = globToRe(m.name); test = (rel, base) => re.test(base); }
  else if (m.glob) { const re = globToRe(m.glob); test = (rel) => re.test(rel); }
  else if (m.regex) { const re = new RegExp(m.regex); test = (rel) => re.test(rel); }
  else throw new Error(`srcs-запись без from/name/glob/regex (root=${m.root})`);

  for (const { p } of walk(root, depth)) {
    const rel = path.relative(root, p);
    if (test(rel, path.basename(p))) results.push({ origin: p, rel, to, mode, root });
  }
  return results;
}

// `srcs` -> плоский список целей {origin, dest, mode}
function resolveTargets(profile, sandboxDir) {
  const defaultRoot = profile.mainSrc ? path.resolve(profile.mainSrc) : process.cwd();
  const targets = [];
  for (const s of (profile.srcs || [])) {
    if (s.from) {
      const origin = path.resolve(s.from);
      const to = s.to || path.basename(origin);
      targets.push({ origin, dest: path.resolve(sandboxDir, to), mode: (s.mode || 'rw').toLowerCase() });
    } else if (s.name || s.glob || s.regex) {
      for (const r of matchEntries(s, defaultRoot)) {
        targets.push({ origin: r.origin, dest: path.resolve(sandboxDir, r.to, r.rel), mode: r.mode });
      }
    } else {
      throw new Error('srcs-запись без from/name/glob/regex');
    }
  }
  return targets;
}

// ---- stat-сигнатура для детекта изменений (дёшево, без чтения контента) ---
function sig(p) {
  let st;
  try { st = fs.lstatSync(p); } catch { return null; }
  if (st.isDirectory()) {
    const h = crypto.createHash('sha256');
    const items = [];
    for (const { p: f } of walk(p, 0)) {
      const s = fs.lstatSync(f);
      items.push(`${path.relative(p, f)}:${Math.floor(s.mtimeMs)}:${s.size}`);
    }
    items.sort();
    h.update(items.join('\n'));
    return 'd:' + h.digest('hex');
  }
  return `f:${Math.floor(st.mtimeMs)}:${st.size}`;
}

function copy(src, dst) {
  fs.mkdirSync(path.dirname(dst), { recursive: true });
  fs.cpSync(src, dst, { recursive: true, force: true });
}

function readManifest(file) {
  try { return JSON.parse(fs.readFileSync(file, 'utf8')); } catch { return []; }
}

// ---- in (pull): origins -> sandbox ----------------------------------------
function copyIn(profileFile, sandboxDir, manifestOut, force) {
  const profile = JSON.parse(fs.readFileSync(profileFile, 'utf8'));
  const prev = new Map(readManifest(manifestOut).map(e => [e.sandboxPath, e]));
  const manifest = [];
  let pulled = 0, kept = 0;
  for (const t of resolveTargets(profile, sandboxDir)) {
    if (fs.existsSync(t.dest) && !force) {
      const p = prev.get(t.dest);
      if (p && sig(t.dest) !== p.destSig) {           // локально изменён -> не трогаем
        console.log(`  KEEP  ${path.relative(sandboxDir, t.dest)} — изменён локально (--force чтобы затереть)`);
        manifest.push(p); kept++; continue;
      }
    }
    copy(t.origin, t.dest);
    manifest.push({ mode: t.mode, origin: t.origin, sandboxPath: t.dest, originSig: sig(t.origin), destSig: sig(t.dest) });
    pulled++;
  }
  fs.writeFileSync(manifestOut, JSON.stringify(manifest, null, 2));
  const rw = manifest.filter(x => x.mode === 'rw').length;
  console.log(`pull: ${pulled} скопировано, ${kept} сохранено; манифест ${manifest.length} (${rw} rw / ${manifest.length - rw} ro)`);
}

// ---- out (push): sandbox -> origins (только rw) ---------------------------
function copyOut(manifestFile, force) {
  const manifest = readManifest(manifestFile);
  let back = 0, missing = 0, skipped = 0;
  for (const e of manifest) {
    if (e.mode !== 'rw') continue;
    if (!fs.existsSync(e.sandboxPath)) { missing++; continue; }
    if (fs.existsSync(e.origin) && !force && e.originSig != null && sig(e.origin) !== e.originSig) {
      console.log(`  SKIP  ${e.origin} — изменён вне песочницы после pull (--force чтобы затереть)`);
      skipped++; continue;
    }
    copy(e.sandboxPath, e.origin);
    e.originSig = sig(e.origin);                       // обновляем базу для следующего push
    back++;
  }
  fs.writeFileSync(manifestFile, JSON.stringify(manifest, null, 2));
  const tail = [missing && `${missing} нет в песочнице`, skipped && `${skipped} пропущено`].filter(Boolean).join(', ');
  console.log(`push: ${back} rw-записей возвращено${tail ? ` (${tail})` : ''}`);
}

// ---- dispatch -------------------------------------------------------------
const [cmd, ...rest] = process.argv.slice(2);
const force = rest.includes('--force');
const pos = rest.filter(a => !a.startsWith('--'));
try {
  if (cmd === 'eval') process.stdout.write(JSON.stringify(evalProfile(pos[0])));
  else if (cmd === 'in') copyIn(pos[0], pos[1], pos[2], force);
  else if (cmd === 'out') copyOut(pos[0], force);
  else { console.error('usage: sandboxer-cfg eval <profile.nix> | in <profileJson> <sandboxDir> <manifestOut> [--force] | out <manifest> [--force]'); process.exit(2); }
} catch (e) {
  console.error('sandboxer-cfg:', e.message);
  process.exit(1);
}
