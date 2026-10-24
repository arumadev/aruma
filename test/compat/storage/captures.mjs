// Compares the captures recorded on Aruma (run.sh captures) with the emulator's recordings in
// test/oracle/storage/emulator, step by step, and prints every difference with its class from
// classified.json ("captures"):
//
//   node captures.mjs <emulator dir> <aruma dir> <classified.json> [--json out.json] [--verbose]
//
// Both sides ran the same script (test/oracle/storage/capture/capture.mjs) with the same
// clients, bucket and rules, so a step of one has its twin in the other by name. What is compared
// for each step, once the run ids, ports, ids and times are written alike:
//
//   result         what the SDK made of it: ok, its error code, or the raw request's status
//   exchanges      the requests the client sent, method and path, as a set
//   x<i>.status    each response's status
//   x<i>.header.H  the headers clients act on (HEADERS below), present or not, and their value
//   x<i>.keys      the fields of a JSON response (and of its metadata, items[0], error) that one
//                  side has and the other lacks: the emulator's own, then Aruma's own ("-": none)
//   x<i>.field.F   the values of the fields clients read (FIELDS below)
//
// A difference is "<scenario> | <step> | <aspect>", with the emulator's value and Aruma's.
// classified.json's "captures" is a list of rules {key, emulator?, aruma?, class, item, note}:
// key and the values are patterns (* matches any run of characters), and the first rule that
// matches a difference classifies it. Each rule is one kind of difference, and the summary counts
// both. It exits 1 when a difference has no class.
import fs from 'node:fs';
import path from 'node:path';

const args = process.argv.slice(2);
const [emuDir, arumaDir, classifiedFile] = args;
const jsonOut = args.includes('--json') ? args[args.indexOf('--json') + 1] : null;
const verbose = args.includes('--verbose'); // every difference under its rule, not one
const glob = (p) => new RegExp(`^${p.split('*').map((s) => s.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*')}$`, 's');
const patterns = (JSON.parse(fs.readFileSync(classifiedFile, 'utf8')).captures ?? []).map((r) => ({
  ...r,
  re: glob(r.key),
  ere: r.emulator === undefined ? null : glob(r.emulator),
  are: r.aruma === undefined ? null : glob(r.aruma),
}));
const matches = (p, d) => p.re.test(d.key) && (!p.ere || p.ere.test(String(d.emulator))) && (!p.are || p.are.test(String(d.aruma)));

const HEADERS = [
  'content-type',
  'content-disposition',
  'content-encoding',
  'content-range',
  'cache-control',
  'x-goog-upload-status',
  'x-goog-upload-size-received',
  'x-goog-upload-url',
  'location',
  'access-control-allow-origin',
  'x-goog-stored-content-encoding',
  'x-http-status-code-override',
  'range',
];
const FIELDS = ['name', 'bucket', 'contentType', 'contentDisposition', 'contentEncoding', 'cacheControl', 'contentLanguage', 'size', 'metageneration', 'customMetadata', 'metadata', 'downloadTokens', 'prefixes', 'items', 'nextPageToken', 'error.code', 'error.message', 'error.errors', 'done', 'kind'];

// norm writes what differs between any two runs alike: the run id, ports, ids and times.
function norm(s) {
  if (typeof s !== 'string') return s;
  return s
    .replace(/aruma-ref(\/|%2F)[a-z0-9-]+-emu\b|aruma-ref(\/|%2F)s3d-\d{8}\b/gi, (m) => m.replace(/(\/|%2F).*/i, (d) => `${d.slice(0, d.startsWith('%') ? 3 : 1)}<run>`))
    .replace(/(127\.0\.0\.1|localhost):\d+/g, '<host>')
    .replace(/<(upload-id|download-token|id-token|uid|email)-[a-z0-9]+>/g, '<$1>')
    .replace(/\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z/g, '<time>')
    .replace(/\b\d{13,16}\b/g, '<generation>')
    .replace(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g, '<uuid>');
}

function jsonBody(x) {
  const t = x?.response?.body?.text;
  if (!t || !/^\s*[{[]/.test(t)) return null;
  try {
    return JSON.parse(t);
  } catch {
    return null;
  }
}

function get(o, dotted) {
  return dotted.split('.').reduce((v, k) => (v && typeof v === 'object' ? v[k] : undefined), o);
}

function resultOf(step) {
  const r = step.result;
  if (!r) return 'none';
  if ('status' in r) return `status ${r.status}`;
  if ('error' in r) return `error ${typeof r.error === 'string' ? norm(r.error).slice(0, 120) : (r.error?.code ?? r.error?.name ?? '?')}`;
  if ('remaining' in r) return `remaining ${r.remaining}`;
  return 'ok';
}

function requestLine(x) {
  const u = sortQuery(norm(x.request.url)).replace(/([?&](upload_id|token|delete_token|pageToken)=)[^&]+/g, '$1<v>');
  return `${x.request.method} ${u}`;
}

// sortQuery puts a URL's query parameters in order: no client reads them in order (A §6.7).
function sortQuery(s) {
  const i = s.indexOf('?');
  if (!/^(https?:\/\/|\/)/.test(s) || i < 0) return s;
  return `${s.slice(0, i)}?${s.slice(i + 1).split('&').sort().join('&')}`;
}

// canonical is JSON with every object's keys in order: no client reads them in order.
function canonical(v) {
  if (Array.isArray(v)) return v.map(canonical);
  if (v && typeof v === 'object') return Object.fromEntries(Object.keys(v).sort().map((k) => [k, canonical(v[k])]));
  return v;
}

function show(v) {
  if (v === undefined) return '(absent)';
  return sortQuery(norm(typeof v === 'string' ? v : JSON.stringify(canonical(v))));
}

function compareStep(scenario, a, b, out) {
  const push = (aspect, emu, aru) => out.push({ key: `${scenario} | ${a.step} | ${aspect}`, emulator: emu, aruma: aru });
  if (resultOf(a) !== resultOf(b)) push('result', resultOf(a), resultOf(b));
  // As a set: the Admin SDKs send a deleteFiles' DELETEs ten at a time, in no set order.
  const ea = a.exchanges.map(requestLine).sort();
  const eb = b.exchanges.map(requestLine).sort();
  if (ea.join('\n') !== eb.join('\n')) push('exchanges', ea.join(' ; '), eb.join(' ; '));
  const n = Math.min(a.exchanges.length, b.exchanges.length);
  for (let i = 0; i < n; i++) {
    const xa = a.exchanges[i];
    const xb = b.exchanges[i];
    if (xa.response.status !== xb.response.status) push(`x${i}.status`, xa.response.status, xb.response.status);
    for (const h of HEADERS) {
      const va = xa.response.headers[h];
      const vb = xb.response.headers[h];
      if (show(va) !== show(vb)) push(`x${i}.header.${h}`, show(va), show(vb));
    }
    const ja = jsonBody(xa);
    const jb = jsonBody(xb);
    if (!ja || !jb || Array.isArray(ja) !== Array.isArray(jb)) {
      if (Boolean(ja) !== Boolean(jb)) push(`x${i}.body`, ja ? 'json' : show(xa.response.body?.text ?? xa.response.body?.length), jb ? 'json' : show(xb.response.body?.text ?? xb.response.body?.length));
      continue;
    }
    for (const sub of ['', 'metadata', 'items.0', 'error']) {
      const oa = sub ? get(ja, sub) : ja;
      const ob = sub ? get(jb, sub) : jb;
      if (!oa || !ob || typeof oa !== 'object' || typeof ob !== 'object' || Array.isArray(oa)) continue;
      // The fields one side has and the other lacks, each side's own.
      const onlyA = Object.keys(oa).filter((k) => !(k in ob)).sort();
      const onlyB = Object.keys(ob).filter((k) => !(k in oa)).sort();
      if (onlyA.length || onlyB.length) push(`x${i}.keys${sub ? '.' + sub : ''}`, onlyA.join(',') || '-', onlyB.join(',') || '-');
    }
    for (const f of FIELDS) {
      // A listing's items by name: their fields are compared on items[0] above.
      const pick = (j) => (f === 'items' && Array.isArray(j.items) ? j.items.map((o) => o?.name) : get(j, f));
      const va = pick(ja);
      const vb = pick(jb);
      if (va !== undefined && vb !== undefined && show(va) !== show(vb)) push(`x${i}.field.${f}`, show(va), show(vb));
    }
  }
}

const diffs = [];
const files = fs.readdirSync(emuDir).filter((f) => f.endsWith('.json'));
let steps = 0;
for (const f of files) {
  const scenario = path.basename(f, '.json');
  const bf = path.join(arumaDir, f);
  if (!fs.existsSync(bf)) {
    diffs.push({ key: `${scenario} | (file) | missing`, emulator: 'recorded', aruma: 'missing' });
    continue;
  }
  const A = JSON.parse(fs.readFileSync(path.join(emuDir, f), 'utf8'));
  const B = JSON.parse(fs.readFileSync(bf, 'utf8'));
  const bySteps = new Map();
  for (const s of B.steps) bySteps.set(norm(s.step), [...(bySteps.get(norm(s.step)) ?? []), s]);
  for (const s of A.steps) {
    steps++;
    const twins = bySteps.get(norm(s.step));
    const t = twins?.shift();
    if (!t) {
      diffs.push({ key: `${scenario} | ${s.step} | step`, emulator: 'present', aruma: 'missing' });
      continue;
    }
    compareStep(scenario, s, t, diffs);
  }
  for (const [name, rest] of bySteps) for (const _ of rest) diffs.push({ key: `${scenario} | ${name} | step`, emulator: 'missing', aruma: 'present' });
}

const hits = new Map(patterns.map((p) => [p, []]));
const unclassified = [];
for (const d of diffs) {
  const p = patterns.find((p) => matches(p, d));
  d.class = p ? { class: p.class, item: p.item, kind: p.note } : null;
  if (p) hits.get(p).push(d);
  else unclassified.push(d);
}
const kinds = {};
const counts = {};
for (const [p, ds] of hits) {
  if (!ds.length) continue;
  kinds[p.class] = (kinds[p.class] ?? 0) + 1;
  counts[p.class] = (counts[p.class] ?? 0) + ds.length;
}
const per = Object.keys(kinds).sort().map((c) => `${c}: ${kinds[c]} kinds, ${counts[c]}`);
if (unclassified.length) per.push(`unclassified: ${unclassified.length}`);
console.log(`captures: ${files.length} scenarios, ${steps} steps, ${diffs.length} differences of ${Object.values(kinds).reduce((a, b) => a + b, 0)} kinds (${per.join('; ')})`);
for (const [p, ds] of hits) {
  if (!ds.length) {
    console.log(`  [rule matched nothing] ${p.key}`);
    continue;
  }
  console.log(`  [${p.class}${p.item ? ' ' + p.item : ''}] ${ds.length}x ${p.note}`);
  for (const d of verbose ? ds : ds.slice(0, 1)) console.log(`      ${verbose ? '' : 'e.g. '}${d.key}${verbose ? `: ${String(d.emulator).slice(0, 80)} => ${String(d.aruma).slice(0, 80)}` : ''}`);
}
for (const d of unclassified) {
  console.log(`  [UNCLASSIFIED] ${d.key}\n      emulator: ${String(d.emulator).slice(0, 300)}\n      aruma:    ${String(d.aruma).slice(0, 300)}`);
}
if (jsonOut) fs.writeFileSync(jsonOut, JSON.stringify(diffs, null, 2) + '\n');
process.exit(unclassified.length ? 1 : 0);
