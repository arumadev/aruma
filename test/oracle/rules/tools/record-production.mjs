// Records topic case modules against the production Rules Test API.
//
//   node record-production.mjs --dev <dir> --heldout <dir> <topic.mjs>...
//
// Each topic's cases are split first (oracle.mjs heldOut) and the two sets
// are recorded in separate API calls, so no request, ruleset or response of
// the development set contains a held-out case. For each set it writes
// <dir>/<topic>.json (normalised cases) and <dir>/raw/<topic>.json (every
// request body and response; requests carry no credentials). See ../README.md.
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { aloneRuleset, classify, fileName, heldOut, issuesText, packRuleset, testCase } from './oracle.mjs';
import { PROJECT, send } from './rulesapi.mjs';

const VERSION = 'firebaserules.googleapis.com v1 projects.test';
const PACK = 60;

function bodyText(service, source, tcs, raw) {
  const body = { source: { files: [{ name: fileName(service), content: source }] } };
  if (tcs.length) body.testSuite = { testCases: tcs };
  let text = JSON.stringify(body);
  for (const [from, to] of raw || []) text = text.split(JSON.stringify(from)).join(to);
  return text;
}

// rawRequest is the request body as recorded. The ruleset text and the test
// cases are in the normalised file (rulesets, ruleset or rulesetGenerated;
// request, resource, functionMocks): here the ruleset is replaced by its
// size in bytes and its SHA-256 (of the UTF-8 text), and each test case by
// its request path, which names it within the call.
function rawRequest(text, gen) {
  const b = JSON.parse(text);
  const f = b.source.files[0];
  f.content = {
    sha256: createHash('sha256').update(f.content).digest('hex'),
    bytes: Buffer.byteLength(f.content),
    ...(gen ? { generated: gen } : {}),
  };
  if (b.testSuite) b.testSuite = { testCasePaths: b.testSuite.testCases.map((t) => t.request.path) };
  return b;
}

async function recordSet(mod, cases, set) {
  const { topic, service, fns } = mod;
  const recordedAt = new Date().toISOString();
  const raw = [];
  const rulesets = {};
  const results = new Map();

  let alone = cases.filter((c) => !c.expr || c.alone);
  let packed = cases.filter((c) => !alone.includes(c));
  let packNo = 0;
  while (packed.length) {
    let chunk = packed.slice(0, PACK);
    packed = packed.slice(PACK);
    for (;;) {
      const { source, lines } = packRuleset(service, fns, chunk, mod.wildcard);
      const tcs = chunk.map((c) => testCase(service, mod.resource, c));
      const text = bodyText(service, source, tcs, chunk.flatMap((c) => c.raw || []));
      const res = await send(text);
      raw.push({ kind: 'pack', request: rawRequest(text), status: res.status, response: res.body });
      const tr = res.body.testResults;
      if (res.status === 200 && tr && tr.length === chunk.length) {
        const ref = `pack-${++packNo}`;
        rulesets[ref] = source;
        chunk.forEach((c, i) => results.set(c.id, { ref, tc: tcs[i], r: tr[i] }));
        break;
      }
      const errs = (res.body.issues || []).filter((i) => i.severity === 'ERROR');
      const bad = chunk.filter((c) =>
        errs.some((i) => i.sourcePosition && i.sourcePosition.line >= lines[c.id][0] && i.sourcePosition.line <= lines[c.id][1]),
      );
      // An error outside the cases' lines, or a refused request: record each alone.
      const moved = bad.length ? bad : chunk;
      alone = alone.concat(moved);
      chunk = chunk.filter((c) => !moved.includes(c));
      if (!chunk.length) break;
    }
  }

  for (const c of alone) {
    const source = aloneRuleset(service, fns, c, mod.wildcard);
    const tc = testCase(service, mod.resource, c);
    const text = bodyText(service, source, [tc], c.raw);
    const res = await send(text);
    raw.push({ kind: 'alone', id: c.id, request: rawRequest(text, c.gen), status: res.status, response: res.body });
    results.set(c.id, { source, tc, res, text });
  }

  const out = [];
  const defaultResource = testCase(service, mod.resource, { id: 'default' }).resource;
  for (const c of cases) {
    const x = results.get(c.id);
    const n = {
      id: `aruma/${topic}/${c.id}`,
      service,
      source: 'aruma',
      provenance: 'rules-test-api',
      recordedAt,
      version: VERSION,
      description: c.desc,
    };
    if (c.expr) n.expression = c.expr;
    if (x.ref) n.rulesetRef = x.ref;
    else if (c.gen) n.rulesetGenerated = c.gen;
    else n.ruleset = x.source;
    n.request = x.tc.request;
    if (x.tc.resource !== undefined) {
      // The topic's default resource is written once, in resources.default.
      if (c.resource === undefined && defaultResource !== undefined) n.resourceRef = 'default';
      else n.resource = x.tc.resource;
    }
    if (x.tc.functionMocks) n.functionMocks = x.tc.functionMocks;
    // In the test case as sent, each JSON number from was written as the
    // text to (a number JSON.stringify cannot write, such as 2.0).
    if (c.raw) n.rawNumbers = c.raw.map(([from, to]) => ({ from, to }));
    let expected;
    if (x.ref) {
      expected = classify(x.r);
      if (x.r.visitedExpressions) n.visited = x.r.visitedExpressions.map((v) => v.value);
    } else if (x.res.status === 200 && x.res.body.testResults) {
      const r = x.res.body.testResults[0];
      expected = classify(r);
      if (r.visitedExpressions) n.visited = r.visitedExpressions.map((v) => v.value);
      const warn = (x.res.body.issues || []).filter((i) => i.severity !== 'ERROR');
      if (warn.length) n.warnings = issuesText(warn);
    } else if (x.res.status === 200 && x.res.body.issues) {
      expected = { outcome: 'error', errorKind: 'compile', message: issuesText(x.res.body.issues) };
    } else {
      const e = x.res.body.error || {};
      expected = {
        outcome: 'error',
        errorKind: 'request',
        message: `${x.res.status} ${e.status || ''} ${e.message || JSON.stringify(x.res.body)}`.replace(/ +/g, ' ').trim(),
      };
    }
    n.expected = expected;
    if (c.emu !== undefined) n.emulator = c.emu === false ? 'skip' : `skip: ${c.emu}`;
    if (c.note) n.note = c.note;
    if (c.emulatorOutcome) n.hidaneEmulatorOutcome = c.emulatorOutcome;
    out.push(n);
  }

  mkdirSync(join(set.dir, 'raw'), { recursive: true });
  const file = {
    schema: 'aruma.rules-oracle/v1',
    topic,
    service,
    set: set.name,
    source: 'aruma',
    provenance: 'rules-test-api',
    project: PROJECT,
    recordedAt,
    version: VERSION,
    rulesets,
    resources: defaultResource === undefined ? {} : { default: defaultResource },
    cases: out,
  };
  writeFileSync(join(set.dir, `${topic}.json`), JSON.stringify(file, null, 1) + '\n');
  writeFileSync(
    join(set.dir, 'raw', `${topic}.json`),
    JSON.stringify({ topic, set: set.name, recordedAt, project: PROJECT, calls: raw }, null, 1) + '\n',
  );
  const count = {};
  for (const n of out) count[n.expected.outcome] = (count[n.expected.outcome] || 0) + 1;
  console.log(`${topic} [${set.name}]: ${out.length} cases, ${raw.length} calls`, JSON.stringify(count));
}

const args = process.argv.slice(2);
const opt = (name) => {
  const i = args.indexOf(name);
  if (i < 0) throw new Error(`missing ${name}`);
  const v = args[i + 1];
  args.splice(i, 2);
  return resolve(v);
};
const dev = { name: 'development', dir: opt('--dev') };
const held = { name: 'heldout', dir: opt('--heldout') };
for (const p of args) {
  const mod = await import(pathToFileURL(resolve(p)).href);
  // A topic recorded by an earlier, interrupted run is kept.
  if (existsSync(join(dev.dir, `${mod.topic}.json`))) {
    console.log(`${mod.topic}: already recorded, skipped`);
    continue;
  }
  const ids = new Set();
  for (const c of mod.cases) {
    if (ids.has(c.id)) throw new Error(`${mod.topic}: duplicate id ${c.id}`);
    ids.add(c.id);
  }
  const h = mod.cases.filter((c) => heldOut(mod.topic, c));
  const d = mod.cases.filter((c) => !h.includes(c));
  if (h.length) await recordSet(mod, h, held);
  await recordSet(mod, d, dev);
}
