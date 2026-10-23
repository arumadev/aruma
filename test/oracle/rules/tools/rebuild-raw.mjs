// Rebuilds the request bodies of raw files recorded before record-production
// kept them (they held only the ruleset's SHA-256 and size and each test
// case's path). Each body is rebuilt from the normalised file the same way
// the recorder builds it, and its ruleset is checked against the SHA-256
// recorded at the time; a call that does not match is left as it was and
// reported. Rebuilt calls are marked rebuilt: true.
//
//   node rebuild-raw.mjs <production dir>
import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileName, generate, packRuleset } from './oracle.mjs';

const dir = process.argv[2];
const sha = (s) => createHash('sha256').update(s).digest('hex');

for (const f of readdirSync(join(dir, 'raw')).filter((n) => n.endsWith('.json')).sort()) {
  const raw = JSON.parse(readFileSync(join(dir, 'raw', f), 'utf8'));
  const norm = JSON.parse(readFileSync(join(dir, f), 'utf8'));
  const service = norm.service;
  const tc = (c) => {
    const t = { expectation: 'ALLOW', request: c.request };
    if (c.resourceRef) t.resource = norm.resources[c.resourceRef];
    else if ('resource' in c) t.resource = c.resource;
    if (c.functionMocks) t.functionMocks = c.functionMocks;
    return t;
  };
  const text = (source, cases) => {
    const body = { source: { files: [{ name: fileName(service), content: source }] } };
    if (cases.length) body.testSuite = { testCases: cases.map(tc) };
    let s = JSON.stringify(body);
    for (const c of cases) for (const { from, to } of c.rawNumbers || []) s = s.split(JSON.stringify(from)).join(to);
    return s;
  };
  const byId = new Map(norm.cases.map((c) => [c.id.split('/').slice(2).join('/'), c]));
  const MATCH = /^ {4}match \/([^/]+)\/\{(\w+)\} \{\n {6}allow ([^:]+): if /m;
  const rebuildPack = (cases) => {
    const kept = Object.values(norm.rulesets).find((t) => !t.includes('{{expr}}'));
    let fns;
    if (kept) {
      const lines = kept.split('\n');
      const first = lines.findIndex((l) => l.startsWith('    match /'));
      if (first > 3) fns = lines.slice(3, first).join('\n');
    }
    const blocks = [];
    let wildcard;
    for (const c of cases) {
      const own = c.rulesetRef ? norm.rulesets[c.rulesetRef] : c.ruleset;
      const seg = c.request.path.replace(/^\/databases\/\(default\)\/documents\//, '').replace(/^\/b\/[^/]+\/o\//, '').split('/')[0];
      const at = own ? own.indexOf(`    match /${seg}/`) : -1;
      const m = at >= 0 ? own.slice(at).match(MATCH) : null;
      if (!m || c.expression === undefined) return null;
      wildcard = m[2];
      blocks.push({ id: c.id, seg, expr: c.expression, allow: m[3] });
    }
    return packRuleset(service, fns, blocks, wildcard).source;
  };
  let rebuilt = 0;
  let failed = 0;
  for (const call of raw.calls) {
    const req = call.request;
    if (req.requestSha256) continue; // recorded as sent already
    const content = req.source.files[0].content;
    let source;
    let cases;
    if (call.kind === 'alone') {
      const c = byId.get(call.id);
      cases = [c];
      source = c.rulesetGenerated ? generate(c.rulesetGenerated) : c.ruleset;
    } else {
      const ref = Object.keys(norm.rulesets).find((k) => sha(norm.rulesets[k]) === content.sha256);
      if (ref) {
        source = norm.rulesets[ref];
        const inPack = norm.cases.filter((c) => c.rulesetRef === ref);
        const byPath = new Map(inPack.map((c) => [c.request.path, c]));
        cases = req.testSuite.testCasePaths.map((p) => byPath.get(p));
      } else {
        // A pack that did not compile: its cases were then recorded alone
        // or in a smaller pack, and its own ruleset was not kept. Build it
        // again from the cases and the topic's functions (the text before
        // the first match block of a kept pack), as the recorder did; the
        // SHA-256 check below says whether that is the ruleset sent.
        const byPath = new Map(norm.cases.map((c) => [c.request.path, c]));
        cases = req.testSuite.testCasePaths.map((p) => byPath.get(p));
        if (cases.some((c) => !c)) {
          failed++;
          continue;
        }
        source = rebuildPack(cases);
        if (!source) {
          failed++;
          continue;
        }
      }
    }
    if (!source || sha(source) !== (content.sha256 ?? sha(source)) || cases.some((c) => !c)) {
      failed++;
      continue;
    }
    const t = text(source, cases);
    const b = JSON.parse(t);
    const gen = cases.length === 1 && cases[0].rulesetGenerated;
    let out = { rebuilt: true, requestSha256: sha(t), body: b };
    // As the recorder: a body that JSON.parse would change is kept as text.
    if (!gen && JSON.stringify(b) !== t) out = { rebuilt: true, requestSha256: sha(t), bodyText: t };
    if (gen) b.source.files[0].content = { generated: gen, sha256: sha(source), bytes: Buffer.byteLength(source) };
    call.request = out;
    rebuilt++;
  }
  writeFileSync(join(dir, 'raw', f), JSON.stringify(raw, null, 1) + '\n');
  console.log(`${f}: ${rebuilt} rebuilt, ${failed} left as recorded`);
}
