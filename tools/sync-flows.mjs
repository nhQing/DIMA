#!/usr/bin/env node
// Đồng bộ flows.html với mã nguồn.
//
// flows.html trỏ tới từng hàm bằng `file:'store.go:523'`. Số dòng đó già đi
// mỗi lần ai đó sửa mã. Script này đọc lại mã nguồn thật, tìm đúng hàm theo
// tên, sửa lại số dòng, rồi nhúng đoạn mã của hàm vào khối SRC để panel chi
// tiết có cái mà hiện.
//
//   node tools/sync-flows.mjs          xem sẽ đổi gì rồi mới ghi
//   node tools/sync-flows.mjs --check  chỉ báo cáo, không ghi (dùng cho CI)
//
// Không cần cài gì, chỉ cần Node 18 trở lên.

import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const TARGET = join(ROOT, 'flows.html');
const CHECK = process.argv.includes('--check');

const MAX_BODY = 30;      // số dòng tối đa của một đoạn mã
const MAX_LEAD = 6;       // số dòng chú thích ngay trên hàm được lấy theo

/* ---------- đọc mã nguồn ---------- */
const cache = new Map();
function source(file){
  if (!cache.has(file)){
    try { cache.set(file, readFileSync(join(ROOT, file), 'utf8').split(/\r?\n/)); }
    catch { cache.set(file, null); }
  }
  return cache.get(file);
}

// `ui:552` trỏ vào ui/index.html; còn lại là tên file ngay ở thư mục gốc.
function pathOf(ref){
  const file = ref.split(':')[0];
  if (file === 'ui') return 'ui/index.html';
  if (/\.(go|html|js)$/.test(file)) return file;
  return null;                       // "docker CLI" và những thứ tương tự
}

/* ---------- tìm hàm ---------- */
// Tên hiển thị trên sơ đồ có thể là Store.Start(), logBuffer.since() hay
// render(info); cái cần tìm luôn là định danh cuối cùng trước dấu ngoặc.
function identOf(fn){
  const bare = fn.split('(')[0].trim();
  const last = bare.split('.').pop();
  return /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(last) ? last : null;
}

function patterns(path, name){
  if (path.endsWith('.go')){
    return [new RegExp(`^func\\s+(\\([^)]*\\)\\s*)?${name}\\s*\\(`)];
  }
  return [
    new RegExp(`^\\s*(async\\s+)?function\\s+${name}\\s*\\(`),
    new RegExp(`^\\s*(const|let|var)\\s+${name}\\s*=`),
  ];
}

// Một cái tên có thể xuất hiện nhiều lần (ui/index.html có hai hàm draw).
// Số dòng đang ghi trong flows.html tuy cũ nhưng vẫn gần đúng, nên lấy chỗ
// gần nó nhất là cách chọn an toàn nhất.
function findLine(path, name, near){
  const lines = source(path);
  if (!lines) return 0;
  const pats = patterns(path, name);
  const hits = [];
  lines.forEach((l, i) => { if (pats.some(p => p.test(l))) hits.push(i + 1); });
  if (!hits.length) return 0;
  if (hits.length === 1 || !near) return hits[0];
  return hits.reduce((a, b) => Math.abs(b - near) < Math.abs(a - near) ? b : a);
}

// Tìm theo selector, cho các nút: '#doBuild' hay '[data-push]'.
// Dòng gắn onclick nói đúng chỗ xử lý hơn là dòng khai báo thẻ.
function findSelector(path, sel, near){
  const lines = source(path);
  if (!lines) return 0;
  const hits = [];
  lines.forEach((l, i) => { if (l.includes(sel)) hits.push({n:i + 1, wire:/onclick|addEventListener/.test(l)}); });
  if (!hits.length) return 0;
  const wired = hits.filter(h => h.wire);
  const pool = (wired.length ? wired : hits).map(h => h.n);
  if (pool.length === 1 || !near) return pool[0];
  return pool.reduce((a, b) => Math.abs(b - near) < Math.abs(a - near) ? b : a);
}

/* ---------- trích đoạn mã ---------- */
function leading(lines, start){
  const out = [];
  for (let i = start - 2; i >= 0 && out.length < MAX_LEAD; i--){
    const l = lines[i];
    if (/^\s*\/\//.test(l)) out.unshift(l); else break;
  }
  return out;
}

// Đếm ngoặc nhọn để biết hàm kết thúc ở đâu. Chuỗi và chú thích cuối dòng bị
// bỏ qua trước khi đếm, nếu không một dấu { trong chuỗi sẽ làm lệch hết.
function stripNoise(line){
  return line
    .replace(/\\./g, '')
    .replace(/'[^']*'|"[^"]*"|`[^`]*`/g, '')
    .replace(/\/\/.*$/, '');
}

function extract(path, line){
  const lines = source(path);
  if (!lines || line < 1 || line > lines.length) return null;
  const head = lines[line - 1];
  const body = [head];
  let depth = 0, opened = false;
  for (const ch of stripNoise(head)){
    if (ch === '{'){ depth++; opened = true; }
    else if (ch === '}') depth--;
  }
  if (opened && depth <= 0) return finish(path, line, leading(lines, line), body, false);

  let truncated = false;
  for (let i = line; i < lines.length; i++){
    if (body.length >= MAX_BODY){ truncated = true; break; }
    const l = lines[i];
    body.push(l);
    for (const ch of stripNoise(l)){
      if (ch === '{'){ depth++; opened = true; }
      else if (ch === '}') depth--;
    }
    if (opened && depth <= 0) break;
  }
  return finish(path, line, leading(lines, line), body, truncated);
}

// Không nhận ra ranh giới hàm thì vẫn cho xem vùng quanh dòng đó, nhưng nói
// rõ là vùng quanh chứ không phải cả hàm.
function around(path, line){
  const lines = source(path);
  if (!lines || line < 1 || line > lines.length) return null;
  const from = Math.max(0, line - 4), to = Math.min(lines.length, line + 5);
  return {
    sig: lines[line - 1].trim(),
    code: dedent(lines.slice(from, to)).join('\n'),
    approx: true,
  };
}

function finish(path, line, lead, body, truncated){
  const code = dedent([...lead, ...body]).join('\n') + (truncated ? '\n…' : '');
  return { sig: body[0].trim(), code, approx: false };
}

function dedent(lines){
  const indents = lines.filter(l => l.trim()).map(l => l.match(/^[\t ]*/)[0].length);
  const cut = indents.length ? Math.min(...indents) : 0;
  return lines.map(l => l.slice(cut));
}

/* ---------- chạy ---------- */
const raw = readFileSync(TARGET, 'utf8');
const B = raw.indexOf('/* SRC-BEGIN */'), E = raw.indexOf('/* SRC-END */');
if (B < 0 || E < 0){
  console.error('Không thấy mốc /* SRC-BEGIN */ … /* SRC-END */ trong flows.html');
  process.exit(1);
}
const before = raw.slice(0, B), after = raw.slice(E + '/* SRC-END */'.length);

const SRC = {};
const moved = [], missing = [], approx = [];

const out = before.split('\n').map(text => {
  const mFile = text.match(/file:'([^']+)'/);
  if (!mFile) return text;
  const ref = mFile[1];
  const path = pathOf(ref);
  if (!path) return text;
  const near = Number(ref.split(':')[1]) || 0;

  const mFn = text.match(/fn:'([^']*)'/);
  const mSel = text.match(/sel:'([^']*)'/);

  let line = 0, name = null;
  if (mFn){
    name = identOf(mFn[1]);
    if (name) line = findLine(path, name, near);
    // fn kiểu '#addProject.onclick' — coi phần đầu là selector.
    if (!line && /^[#[]/.test(mFn[1])) line = findSelector(path, mFn[1].split('.')[0], near);
  } else if (mSel){
    line = findSelector(path, mSel[1], near);
  }

  const label = mFn ? mFn[1] : (mSel ? mSel[1] : ref);
  if (!line){
    missing.push(`${label}  (${ref})`);
    if (mFn && near){
      const a = around(path, near);
      if (a){ SRC[mFn[1]] = a; approx.push(label); }
    }
    return text;
  }

  const fresh = `${ref.split(':')[0]}:${line}`;
  if (fresh !== ref) moved.push(`${label}  ${ref} → ${fresh}`);

  if (mFn){
    const snip = extract(path, line);
    if (snip) SRC[mFn[1]] = snip;
  }
  return text.replace(/file:'[^']+'/, `file:'${fresh}'`);
}).join('\n');

const stamp = new Date().toISOString().slice(0, 16).replace('T', ' ');
const block =
  '/* SRC-BEGIN */\n' +
  `const SRC_STAMP = '${stamp}';\n` +
  'const SRC = ' + JSON.stringify(SRC, null, 0) + ';\n' +
  '/* SRC-END */';

console.log(`Hàm trích được mã nguồn: ${Object.keys(SRC).length}`);
if (moved.length){
  console.log(`\nSố dòng đã đổi (${moved.length}):`);
  moved.forEach(m => console.log('  ' + m));
}
if (approx.length){
  console.log(`\nKhông rõ ranh giới hàm, chỉ lấy vùng quanh dòng (${approx.length}):`);
  approx.forEach(m => console.log('  ' + m));
}
if (missing.length){
  console.log(`\nKhông tìm thấy trong mã nguồn — giữ nguyên dòng đang ghi (${missing.length}):`);
  missing.forEach(m => console.log('  ' + m));
}

if (CHECK){
  console.log(moved.length ? '\n--check: flows.html đã cũ so với mã nguồn.' : '\n--check: flows.html vẫn khớp.');
  process.exit(moved.length ? 1 : 0);
}

writeFileSync(TARGET, out + block + after, 'utf8');
console.log('\nĐã ghi flows.html.');
