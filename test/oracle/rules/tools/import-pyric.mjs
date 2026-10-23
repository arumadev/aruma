// Imports Pyric's Security Rules observations (github.com/davideast/pyric,
// Apache-2.0) into ../imported/pyric/.
//
//   node import-pyric.mjs <pyric checkout> <commit>
//
// original/ keeps the observation files and the corpus scenarios they were
// recorded from byte for byte. normalised/ holds one file per service in the
// aruma.rules-oracle/v1 schema: each corpus case joined with the verdict the
// observation recorded for it. The corpus files are TypeScript; they are
// loaded with Node's type stripping (they export plain data).
import { copyFileSync, mkdirSync, readdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const OUT = join(HERE, '..', 'imported', 'pyric');
const [repo, commit] = process.argv.slice(2).map((a, i) => (i === 0 ? resolve(a) : a));
const CONF = join(repo, 'packages/conformance');
const DEFAULT_BUCKET = 'demo-pyric.appspot.com';
const DB = '/databases/(default)/documents/';

const docPath = (p) => DB + (p.startsWith('/') ? p.slice(1) : p);
const objPath = (p, bucket = DEFAULT_BUCKET) =>
  p.startsWith('/b/') && p.includes('/o/') ? p : `/b/${bucket}/o/${p.startsWith('/') ? p.slice(1) : p}`;

function mock(service, m) {
  const fn = service === 'firestore' ? m.function : `firestore.${m.function}`;
  const out = { function: fn, args: [{ exactValue: docPath(m.path) }] };
  out.result = m.function === 'get' ? { value: { data: m.result } } : { value: m.result === true };
  return out;
}

// The Rules Test API test case Pyric sends for a corpus case
// (packages/pyric/src/rules/test/spec.ts buildApiTestCase and
// buildStorageApiTestCase).
function wire(service, tc) {
  const request = { method: tc.method };
  if (tc.auth) request.auth = { uid: tc.auth.uid, token: tc.auth.token ?? {} };
  if (service === 'firestore') {
    request.path = docPath(tc.path);
    if (tc.data) request.resource = { data: tc.data };
    if (tc.requestTime) request.time = tc.requestTime;
    if (tc.query) {
      const { where, ...q } = tc.query;
      if (Object.keys(q).length > 0 || where === undefined) request.query = q;
    }
  } else {
    request.path = objPath(tc.path, tc.bucket);
    if (tc.resource) request.resource = tc.resource;
    if (tc.requestTime) request.time = tc.requestTime;
  }
  const out = { request };
  if (service === 'firestore' && tc.resource) out.resource = { data: tc.resource };
  if (service === 'storage' && tc.existingResource) out.resource = tc.existingResource;
  if (tc.functionMocks && tc.functionMocks.length) out.functionMocks = tc.functionMocks.map((m) => mock(service, m));
  return out;
}

function outcome(verdict, diag) {
  const notes = (diag && diag.notes) || [];
  const errs = notes.filter((n) => typeof n === 'string' && n.startsWith('Error:'));
  if (verdict === 'ALLOW') return { outcome: 'allow' };
  if (verdict === 'DENY') {
    if (errs.length || (diag && diag.api && diag.api.errorPosition)) {
      return { outcome: 'error', errorKind: 'evaluation', message: errs.join('\n') || undefined };
    }
    return { outcome: 'deny' };
  }
  if (verdict === 'RULES_ERROR') return { outcome: 'error', errorKind: 'compile', message: notes.join('\n') || undefined };
  if (!['ALLOW', 'DENY', 'RULES_ERROR', 'INVALID_ARGUMENT'].includes(verdict)) {
    // Not a rules decision: a relation or a measurement the probe recorded
    // (such as "generation preserved across a metadata update", a time).
    return { outcome: 'fact', value: verdict };
  }
  if (verdict === 'INVALID_ARGUMENT') return { outcome: 'error', errorKind: 'request', message: notes.join('\n') || undefined };
}

function flatten(behavior, prefix = '') {
  const out = [];
  for (const [k, v] of Object.entries(behavior)) {
    if (v && typeof v === 'object') out.push(...flatten(v, `${prefix}${k}/`));
    else out.push([`${prefix}${k}`, v]);
  }
  return out;
}

const normalised = { firestore: [], storage: [] };
const rulesets = { firestore: {}, storage: {} };
const counts = {};
for (const [dir, service, prefix] of [
  ['firestore-rules', 'firestore', 'rules-firestore-'],
  ['storage-rules', 'storage', 'rules-storage-'],
]) {
  const obsDir = join(CONF, 'observations', dir);
  mkdirSync(join(OUT, 'original/observations', dir), { recursive: true });
  mkdirSync(join(OUT, 'original/rules-corpus', service), { recursive: true });
  for (const f of readdirSync(obsDir).filter((n) => n.endsWith('.json')).sort()) {
    copyFileSync(join(obsDir, f), join(OUT, 'original/observations', dir, f));
    const obs = JSON.parse(readFileSync(join(obsDir, f), 'utf8'));
    const base = {
      service,
      source: 'pyric',
      recordedAt: obs.observedAt,
      version: `Pyric ${commit.slice(0, 12)}; firebase JS SDK ${obs.fbSdkVersion}`,
    };
    if (f.startsWith(prefix)) {
      const id = f.slice(prefix.length, -'.json'.length);
      const src = join(CONF, 'rules-corpus', service, `${id}.ts`);
      copyFileSync(src, join(OUT, 'original/rules-corpus', service, `${id}.ts`));
      const { scenario } = await import(pathToFileURL(src).href);
      rulesets[service][id] = scenario.rules;
      scenario.cases.forEach((tc, i) => {
        const verdict = obs.behavior[tc.description];
        const n = {
          id: `pyric/${service}/${id}/${i}`,
          ...base,
          provenance: 'rules-test-api',
          description: tc.description,
          rulesetRef: id,
          ...wire(service, tc),
          expected: verdict === undefined ? { outcome: 'unknown', message: 'no verdict in the observation' } : outcome(verdict, obs.diagnostics?.[tc.description]),
          pyricExpectation: tc.expectation,
          observation: `original/observations/${dir}/${f}`,
        };
        normalised[service].push(n);
      });
    } else {
      // stdlib-* files: probes whose rules live in Pyric's runners
      // (packages/conformance/src/run-storage-stdlib*.ts), not in the corpus.
      // Their verdicts are kept with the probe's own keys.
      const deployed = f.startsWith('stdlib-realstorage-');
      if (typeof obs.probeRules === 'string') rulesets[service][f.slice(0, -5)] = obs.probeRules;
      for (const [key, verdict] of flatten(obs.behavior)) {
        normalised[service].push({
          id: `pyric/${service}/${f.slice(0, -5)}/${key}`,
          ...base,
          provenance: deployed ? 'deployed' : 'rules-test-api',
          description: `${obs.name}: ${key}`,
          rulesetRef: typeof obs.probeRules === 'string' ? f.slice(0, -5) : undefined,
          rulesetNote: obs.probeRules && typeof obs.probeRules === 'string'
            ? undefined
            : `the rules are built by Pyric's runner (packages/conformance/src/run-storage-stdlib${deployed ? '-real' : ''}.ts at ${commit.slice(0, 12)}); see the observation file`,
          expected: outcome(verdict, obs.diagnostics?.[key]),
          observation: `original/observations/${dir}/${f}`,
        });
      }
    }
  }
  // The corpus math cases are shared by two scenarios.
  const math = join(CONF, 'src/rules-math-cases.ts');
  if (existsSync(math)) {
    mkdirSync(join(OUT, 'original/src'), { recursive: true });
    copyFileSync(math, join(OUT, 'original/src/rules-math-cases.ts'));
  }
}

mkdirSync(join(OUT, 'normalised'), { recursive: true });
for (const service of ['firestore', 'storage']) {
  const cases = normalised[service];
  for (const c of cases) counts[`${service} ${c.provenance} ${c.expected.outcome}`] = (counts[`${service} ${c.provenance} ${c.expected.outcome}`] || 0) + 1;
  writeFileSync(
    join(OUT, 'normalised', `${service}-rules.json`),
    JSON.stringify(
      {
        schema: 'aruma.rules-oracle/v1',
        service,
        source: 'pyric',
        upstream: `https://github.com/davideast/pyric/tree/${commit}/packages/conformance`,
        licence: 'Apache-2.0',
        rulesets: rulesets[service],
        cases,
      },
      null,
      1,
    ) + '\n',
  );
}
console.log(JSON.stringify(counts, null, 1));
