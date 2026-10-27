// F5 production recordings for the Document Database's security rules (ADR-021
// and the F5 plan): what production Cloud Firestore answers an END USER under
// rules the recorder deploys, over REST and gRPC.
//
//   f5-rules-deployed.json   the rules source the other files ran under, and the
//                            release and user claims they ran with
//   f5-rules-methods.json    allow and deny for get, list, create, update and
//                            delete (read, write and the split methods), the
//                            request.method values, and precondition versus
//                            rules ordering
//   f5-rules-auth.json       request.auth: uid, token fields, email_verified,
//                            sign_in_provider, anonymous and unauthenticated
//   f5-rules-request.json    request.resource.data against resource.data (masks,
//                            transforms, deletes, creates of an existing
//                            document), request.time, request.path and
//                            resource.__name__ / resource.id
//   f5-rules-lookups.json    get, exists, getAfter, existsAfter, the 10 and 20
//                            lookup limits, lookups of a missing document
//   f5-rules-queries.json    list rules against queries: resource.data
//                            constraints, request.query, or and in queries,
//                            collection groups, aggregations
//   f5-rules-batches.json    a failing write inside a batch, a BatchWrite and
//                            transactions (reads and writes)
//   f5-rules-listen.json     Listen and Write streams under rules
//   f5-rules-cleanup.json    the deletion of everything written, verified
//
//   node record-f5-rules.mjs [--only=methods,auth,...]
//
// The owner's access token bypasses rules, so it only seeds and reads back
// state. Every case is sent with an end-user ID token (Identity Toolkit sign-in
// of test users the recorder creates and deletes), or with the project's web API
// key alone for the unauthenticated cases. The tokens and the API key are never
// printed or written.
//
// What it touches in the project: the cloud.firestore rules release (the
// original ruleset is restored at the end, and what was released is verified
// afterwards), three Auth test users, and documents under the top-level
// collections aruma-ref-f5-*.
//
// Environment: FIRESTORE_PROTOS (a build/protos directory of @google-cloud/firestore),
// NODE_OPTIONS=--dns-result-order=ipv4first, F5_API_KEY_FILE (a file holding the web API key),
// F5_ORIGINAL_RULESET (the full name of the ruleset released before the run).
import { randomBytes } from 'node:crypto';
import { readFileSync } from 'node:fs';
import {
  PROJECT, DB, ROOT, PREFIX, rest, v, fields, versions, writeCorpus, assertScoped, assertMessageScoped, normalise, compact,
  grpcAvailable, grpcUnary, grpcStream, grpcDuplex, grpcVersions, identity, accessToken,
} from './lib.mjs';

const GENERATOR = 'test/oracle/documents/production/record-f5-rules.mjs';
const P = 'aruma-ref-f5';
const col = (n) => `${P}-${n}`;
const log = (s) => process.stderr.write(s);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const relProject = (s) => s.split(`projects/${PROJECT}/`).join('projects/{project}/');
const rel = (name) => name.replace(`${ROOT}/`, '');
const only = (process.argv.find((a) => a.startsWith('--only=')) || '').slice(7).split(',').filter(Boolean);
const wanted = (t) => !only.length || only.includes(t);
const API_KEY = readFileSync(process.env.F5_API_KEY_FILE, 'utf8').trim();
const ORIGINAL_RULESET = process.env.F5_ORIGINAL_RULESET;
const RULES_API = 'https://firebaserules.googleapis.com/v1';
const TOOLKIT = 'https://identitytoolkit.googleapis.com/v1';

// ---- rules ----

const FX = (i) => `/databases/$(database)/documents/${col('fx')}/${i}`;
const L2 = (d) => `/databases/$(database)/documents/${col('l2')}/${d}`;
const gets = (n, kind = 'get', start = 1) =>
  Array.from({ length: n }, (_, i) => {
    const p = FX(start + i);
    return kind === 'get' ? `get(${p}).data.v == 1` : kind === 'getAfter' ? `getAfter(${p}).data.v == 1` : kind === 'exists' ? `exists(${p})` : `existsAfter(${p})`;
  }).join(' && ');
const same = (n) => Array.from({ length: n }, () => `get(${FX(1)}).data.v == 1`).join(' && ');
const or = (arr) => arr.map((x) => `(${x})`).join('\n        || ');
const T = (path) => `/databases/$(database)/documents/${path}`;

function buildRules({ uidA, uidB, emailA }) {
  const authRules = or([
    `d == 'authnull' && request.auth == null`,
    `d == 'authnotnull' && request.auth != null`,
    `d == 'uid-a' && request.auth.uid == '${uidA}'`,
    `d == 'uid-b' && request.auth.uid == '${uidB}'`,
    `d == 'uid-is-string' && request.auth.uid is string`,
    `d == 'email' && request.auth.token.email == '${emailA}'`,
    `d == 'verified-false' && request.auth.token.email_verified == false`,
    `d == 'verified-true' && request.auth.token.email_verified == true`,
    `d == 'verified-bool' && request.auth.token.email_verified is bool`,
    `d == 'prov-password' && request.auth.token.firebase.sign_in_provider == 'password'`,
    `d == 'prov-anonymous' && request.auth.token.firebase.sign_in_provider == 'anonymous'`,
    `d == 'ident-email' && request.auth.token.firebase.identities.email[0] == '${emailA}'`,
    `d == 'sub-is-uid' && request.auth.token.sub == request.auth.uid`,
    `d == 'aud' && request.auth.token.aud == '${PROJECT}'`,
    `d == 'iss' && request.auth.token.iss == 'https://securetoken.google.com/${PROJECT}'`,
    `d == 'auth-time-int' && request.auth.token.auth_time is int`,
    `d == 'iat-int' && request.auth.token.iat is int`,
    `d == 'exp-int' && request.auth.token.exp is int`,
    `d == 'firebase-map' && request.auth.token.firebase is map`,
    `d == 'token-map' && request.auth.token is map`,
    `d.matches('tok_.*') && d.split('_')[1] in request.auth.token`,
    `d.matches('fb_.*') && d.split('_')[1] in request.auth.token.firebase`,
    `d == 'auth-keys' && request.auth.keys().hasAll(['uid', 'token'])`,
    `d == 'auth-keys-only' && request.auth.keys().hasOnly(['uid', 'token'])`,
  ]);
  const rrWrite = or([
    // updates of E = {a:1, b:1, m:{x:1,y:2}, arr:[1], n:5}
    `d == 'u-mask-merge' && request.resource.data.a == 2 && request.resource.data.b == 1 && resource.data.a == 1`,
    `d == 'u-res-intact' && resource.data.keys().size() == 5 && resource.data.n == 5 && resource.data.a == 1`,
    `d == 'u-mask-keys' && request.resource.data.keys().hasAll(['a', 'b', 'm', 'arr', 'n']) && request.resource.data.keys().size() == 5`,
    `d == 'u-mask-affected' && request.resource.data.diff(resource.data).affectedKeys() == ['a'].toSet()`,
    `d == 'u-mask-delete' && !('b' in request.resource.data) && request.resource.data.a == 1`,
    `d == 'u-mask-nested' && request.resource.data.m == {'x': 9, 'y': 2}`,
    `d == 'u-nomask' && !('b' in request.resource.data) && request.resource.data.a == 2 && request.resource.data.keys().size() == 1`,
    `d == 'u-ts-eq-time' && request.resource.data.t == request.time`,
    `d == 'u-ts-type' && request.resource.data.t is timestamp`,
    `d == 'u-ts-absent' && !('t' in request.resource.data)`,
    `d == 'u-ts-with-field' && request.resource.data.a == 2 && request.resource.data.t == request.time`,
    `d == 'u-ts-keys' && request.resource.data.keys().hasAll(['a', 'b', 'm', 'arr', 'n', 't'])`,
    `d == 'u-inc-8' && request.resource.data.n == 8`,
    `d == 'u-inc-5' && request.resource.data.n == 5`,
    `d == 'u-inc-3' && request.resource.data.n == 3`,
    `d == 'u-inc-absent' && !('n' in request.resource.data)`,
    `d == 'u-inc-int' && request.resource.data.n is int`,
    `d == 'u-inc-double' && request.resource.data.n == 5.5`,
    `d == 'u-inc-diff' && request.resource.data.diff(resource.data).affectedKeys() == ['n'].toSet()`,
    `d == 'u-max-9' && request.resource.data.n == 9`,
    `d == 'u-max-5' && request.resource.data.n == 5`,
    `d == 'u-min-2' && request.resource.data.n == 2`,
    `d == 'u-au-merged' && request.resource.data.arr == [1, 2]`,
    `d == 'u-au-sent' && request.resource.data.arr == [2]`,
    `d == 'u-au-before' && request.resource.data.arr == [1]`,
    `d == 'u-ra-removed' && request.resource.data.arr == []`,
    // creates
    `d == 'c-res-null' && resource == null`,
    `d == 'c-data-eq' && request.resource.data.a == 1`,
    `d == 'c-keys' && request.resource.data.keys() == ['a'].toSet()`,
    `d == 'c-ts-eq-time' && request.resource.data.t == request.time`,
    `d == 'c-ts-type' && request.resource.data.t is timestamp`,
    `d == 'c-inc-3' && request.resource.data.n == 3`,
    `d == 'c-inc-absent' && !('n' in request.resource.data)`,
    `d == 'c-nested-ts' && request.resource.data.m.t == request.time`,
    `d == 'c-req-res-id' && request.resource.id == d`,
    `d == 'c-keys-time-first' && request.resource.data.keys().hasOnly(['a', 't'])`,
  ]);
  const rrDelete = or([
    `d == 'd-reqres-null' && request.resource == null`,
    `d == 'd-res-a' && resource.data.a == 1`,
    `d == 'd-res-id' && resource.id == d`,
    `d == 'd-res-name' && resource.__name__ == ${T(`${col('r')}/$(d)`)}`,
    `d == 'd-path' && request.path == ${T(`${col('r')}/$(d)`)}`,
  ]);
  const tGet = or([
    `d == 'path' && request.path == ${T(`${col('t')}/path`)}`,
    `d == 'name' && resource.__name__ == ${T(`${col('t')}/name`)}`,
    `d == 'id' && resource.id == d`,
    `d == 'id-data' && resource.data.id == null`,
    `d == 'time-get' && request.time > timestamp.date(2025, 1, 1) && request.time < timestamp.date(2036, 1, 1)`,
    `d == 'time-type' && request.time is timestamp`,
    `d == 'path-size' && request.path.size() == 6`,
    `d == 'path-slice' && request.path[2] == '(default)'`,
  ]);
  const tWrite = or([
    `d == 'c-path' && request.path == ${T(`${col('t')}/c-path`)}`,
    `d == 'c-req-name' && request.resource.__name__ == ${T(`${col('t')}/c-req-name`)}`,
    `d == 'c-req-id' && request.resource.id == d`,
    `d == 'c-time' && request.time > timestamp.date(2025, 1, 1) && request.time < timestamp.date(2036, 1, 1)`,
    `d == 'c-clock' && math.abs(request.time.toMillis() - request.resource.data.c.toMillis()) < 120000`,
    `d == 'c-time-year' && request.time.year() >= 2026`,
    `d == 'u-path' && request.path == ${T(`${col('t')}/u-path`)} && resource.__name__ == request.path`,
    `d == 'u-res-name' && resource.__name__ == ${T(`${col('t')}/u-res-name`)} && resource.id == d`,
    `d == 'u-req-name' && request.resource.__name__ == request.path`,
  ]);
  const tDelete = or([`d == 'd-path' && request.path == ${T(`${col('t')}/d-path`)}`, `d == 'd-res-name' && resource.__name__ == request.path`]);
  const l1 = or([
    `d.matches('g10.*') && ${gets(10)}`,
    `d.matches('g11.*') && ${gets(11)}`,
    `d.matches('g20.*') && ${gets(20)}`,
    `d.matches('g21.*') && ${gets(21)}`,
    `d == 'e10' && ${gets(10, 'exists')}`,
    `d == 'e11' && ${gets(11, 'exists')}`,
    `d == 'mix11' && ${gets(5)} && ${gets(6, 'exists', 6)}`,
    `d == 'same10' && ${same(10)}`,
    `d == 'same11' && ${same(11)}`,
    `d == 'ga10' && ${gets(10, 'getAfter')}`,
    `d == 'ga11' && ${gets(11, 'getAfter')}`,
    `d == 'lk-get' && get(${FX(1)}).data.v == 1`,
    `d == 'lk-get-val' && get(${FX(2)}).data.s == 'two'`,
    `d == 'lk-get-id' && get(${FX(2)}).id == '2'`,
    `d == 'lk-get-name' && get(${FX(2)}).__name__ == ${T(`${col('fx')}/2`)}`,
    `d == 'lk-exists-true' && exists(${FX(1)})`,
    `d == 'lk-exists-false' && !exists(${FX('none')})`,
    `d == 'lk-get-missing' && get(${FX('none')}).data.v == 1`,
    `d == 'lk-get-missing-or' && (get(${FX('none')}).data.v == 1 || true)`,
    `d == 'lk-get-missing-not' && !(get(${FX('none')}).data.v == 1)`,
    `d == 'lk-exists-guard' && exists(${FX('none')}) && get(${FX('none')}).data.v == 1`,
    `d == 'lk-ga' && getAfter(${FX(1)}).data.v == 1`,
    `d == 'lk-ea' && existsAfter(${FX(1)})`,
    `d == 'lk-ea-missing' && !existsAfter(${FX('none')})`,
    `d == 'lk-ga-missing' && getAfter(${FX('none')}).data.v == 1`,
    `d == 'lk-get-dyn' && get(/databases/$(database)/documents/${col('fx')}/$(string(1))).data.v == 1`,
    `d == 'lk-get-self' && get(${T(`${col('l1')}/lk-get-self`)}).data.p == 1`,
    `d == 'lk-get-denied' && get(${T(`${col('m8')}/denied`)}).data.v == 1`,
  ]);
  const l2 = or([
    `d.matches('g10.*') && ${gets(10)}`,
    `d.matches('g11.*') && ${gets(11)}`,
    `d.matches('g20.*') && ${gets(20)}`,
    `d.matches('g21.*') && ${gets(21)}`,
    `d == 'self-ga' && ${gets(10)} && getAfter(${L2('self-ga')}).data.a == 1`,
    `d == 'self-ea' && ${gets(10)} && existsAfter(${L2('self-ea')})`,
    `d == 'self-ex' && ${gets(10)} && !exists(${L2('self-ex')})`,
    `d == 'self-get' && ${gets(10)} && get(${L2('self-get')}).data.a == 0`,
    `d == 'pk-ga' && ${gets(10)} && existsAfter(${L2('g10-a')})`,
    `d == 'pk-ex' && ${gets(10)} && (exists(${L2('g10-a')}) || true)`,
    `d == 'ga-after' && getAfter(${T(`${col('l2t')}/t1`)}).data.v == 2 && get(${T(`${col('l2t')}/t1`)}).data.v == 1`,
    `d == 'ga-noop' && getAfter(${T(`${col('l2t')}/t1`)}).data.v == 1 && existsAfter(${T(`${col('l2t')}/t1`)})`,
    `d == 'ga-create' && existsAfter(${T(`${col('l2t')}/new`)}) && !exists(${T(`${col('l2t')}/new`)}) && getAfter(${T(`${col('l2t')}/new`)}).data.v == 7`,
    `d == 'ga-delete' && !existsAfter(${T(`${col('l2t')}/t1`)}) && exists(${T(`${col('l2t')}/t1`)})`,
    `d == 'ga-inc' && getAfter(${T(`${col('l2t')}/t1`)}).data.n == 8 && get(${T(`${col('l2t')}/t1`)}).data.n == 5`,
    `d == 'ga-ts' && getAfter(${T(`${col('l2t')}/t1`)}).data.t == request.time`,
    `d == 'ga-self' && getAfter(${L2('ga-self')}).data.a == request.resource.data.a && !exists(${L2('ga-self')})`,
    `d == 'ga-peer-denied' && getAfter(${T(`${col('l2t')}/locked`)}).data.v == 2`,
    `d == 'ga-tx' && getAfter(${T(`${col('l2t')}/t1`)}).data.v == 2 && get(${T(`${col('l2t')}/t1`)}).data.v == 1`,
  ]);
  return `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    // methods
    match /${col('m1')}/{d} {
      allow read, write: if d.matches('ok.*');
    }
    match /${col('m2')}/{d} {
      allow get: if d.matches('ok.*');
      allow create: if d.matches('ok.*');
      allow delete: if d.matches('ok.*');
    }
    match /${col('m3')}/{d} {
      allow list: if true;
      allow update: if d.matches('ok.*');
    }
    match /${col('m4')}/{d} {
      allow get: if request.method == d;
      allow list: if request.method == 'list';
      allow create: if request.method == d;
      allow update: if request.method == d;
      allow delete: if request.method == d;
    }
    match /${col('m5')}/{d} {
      allow read: if true;
      allow create: if true;
    }
    match /${col('m6')}/{d} {
      allow read: if true;
    }
    match /${col('m7')}/{d} {
      allow get: if resource.data.x == 1;
    }
    match /${col('m8')}/{d} {
      allow get: if true;
    }
    match /${col('m9')}/{d} {
      allow update: if true;
    }
    // request.auth
    match /${col('a')}/{d} {
      allow get: if ${authRules};
    }
    // request.resource and resource
    match /${col('r')}/{d} {
      allow create, update: if ${rrWrite};
      allow delete: if ${rrDelete};
      allow get: if true;
    }
    // request.time, request.path, resource.__name__
    match /${col('t')}/{d} {
      allow get: if ${tGet};
      allow list: if request.path == ${T(col('t'))};
      allow create, update: if ${tWrite};
      allow delete: if ${tDelete};
    }
    // lookups
    match /${col('fx')}/{d} {
      allow read: if false;
    }
    match /${col('l1')}/{d} {
      allow get: if ${l1};
    }
    match /${col('l2')}/{d} {
      allow write: if ${l2};
    }
    match /${col('l2t')}/{d} {
      allow read, write: if d != 'locked';
    }
    match /${col('l3')}/{d} {
      allow list: if ${gets(10)};
    }
    match /${col('l4')}/{d} {
      allow list: if ${gets(11)};
    }
    // queries
    match /${col('q1')}/{d} {
      allow list: if resource.data.x == 1;
    }
    match /${col('q2')}/{d} {
      allow list: if request.query.limit <= 3;
    }
    match /${col('q3')}/{d} {
      allow list: if request.query.offset <= 1;
    }
    match /${col('q4')}/{d} {
      allow list: if 'limit' in request.query;
    }
    match /${col('q5')}/{d} {
      allow list: if 'offset' in request.query;
    }
    match /${col('q6')}/{d} {
      allow list: if 'orderBy' in request.query;
    }
    match /${col('q7')}/{d} {
      allow list: if request.query.orderBy.size() == 1;
    }
    match /${col('q8')}/{d} {
      allow list: if request.query.orderBy.x == 'ASCENDING';
    }
    match /${col('q9')}/{d} {
      allow list: if request.query.limit <= 2 && resource.data.x == 1;
    }
    match /${col('q10')}/{d} {
      allow list: if resource.data.x in [1, 2];
    }
    match /${col('q11')}/{d} {
      allow list: if resource.data.x == 1 || resource.data.y == 1;
    }
    match /${col('q12')}/{d} {
      allow list: if request.query.limit == null;
    }
    match /${col('q13')}/{d} {
      allow list: if request.query.orderBy[0] == 'x';
    }
    match /{path=**}/${col('cg')}/{d} {
      allow read: if resource.data.pub == true;
    }
    match /${col('cg2')}/{d} {
      allow read: if resource.data.pub == true;
    }
    // listen
    match /${col('ls')}/{d} {
      allow get: if d.matches('ok.*');
      allow list: if resource.data.pub == true;
    }
    match /${col('ls2f')}/{d} {
      allow read, write: if false;
    }
    match /${col('ls2')}/{d} {
      allow list: if get(${T(`${col('ls2f')}/flag`)}).data.open == true;
    }
  }
}
`;
}

// ---- identity ----

const state = { users: {}, uids: [] };
const tokenOf = {};
const issued = {};

async function toolkit(path, body, owner = false) {
  const url = owner ? `${TOOLKIT}/${path}` : `${TOOLKIT}/${path}?key=${API_KEY}`;
  const headers = { 'Content-Type': 'application/json' };
  if (owner) {
    headers.Authorization = `Bearer ${await accessToken()}`;
    headers['x-goog-user-project'] = PROJECT;
  }
  const r = await fetch(url, { method: 'POST', headers, body: JSON.stringify(body) });
  return { http: r.status, body: await r.json().catch(() => ({})) };
}

async function makeUsers(tag) {
  const pw = `Aa1!${randomBytes(12).toString('hex')}`;
  for (const [name, email] of [['A', `${P}-${tag}-a@example.com`], ['B', `${P}-${tag}-b@example.com`]]) {
    const r = await toolkit('accounts:signUp', { email, password: pw, returnSecureToken: true });
    if (r.http !== 200) throw new Error(`signUp ${name}: HTTP ${r.http} ${r.body?.error?.message}`);
    state.users[name] = { email, uid: r.body.localId, pw };
    state.uids.push(r.body.localId);
    tokenOf[name] = r.body.idToken;
    issued[name] = Date.now();
  }
  const r = await toolkit('accounts:signUp', { returnSecureToken: true });
  if (r.http !== 200) throw new Error(`signUp anonymous: HTTP ${r.http}`);
  state.users.anon = { uid: r.body.localId };
  state.uids.push(r.body.localId);
  tokenOf.anon = r.body.idToken;
  issued.anon = Date.now();
}

async function idToken(actor) {
  const who = actor === 'user' ? 'A' : actor === 'userB' ? 'B' : actor;
  if (who === 'anon') return tokenOf.anon; // an anonymous user cannot sign in again: its token lasts the hour
  if (Date.now() - issued[who] > 40 * 60_000) {
    const u = state.users[who];
    const r = await toolkit('accounts:signInWithPassword', { email: u.email, password: u.pw, returnSecureToken: true });
    if (r.http !== 200) throw new Error(`signIn ${who}: HTTP ${r.http}`);
    tokenOf[who] = r.body.idToken;
    issued[who] = Date.now();
  }
  return tokenOf[who];
}

function claimNames(tok) {
  const p = JSON.parse(Buffer.from(tok.split('.')[1], 'base64url').toString('utf8'));
  return { top: Object.keys(p).sort(), firebase: Object.keys(p.firebase || {}).sort(), sign_in_provider: p.firebase?.sign_in_provider, identityKeys: Object.keys(p.firebase?.identities || {}).sort(), email_verified: p.email_verified };
}

async function as(actor) {
  identity.bearer = null;
  identity.none = false;
  identity.apiKey = null;
  if (actor === 'owner') return;
  if (actor === 'none') {
    identity.none = true;
    identity.apiKey = API_KEY;
    return;
  }
  identity.bearer = await idToken(actor);
}

// ---- rules deployment ----

async function rulesCall(method, path, body) {
  const r = await fetch(`${RULES_API}/${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${await accessToken()}`, 'x-goog-user-project': PROJECT },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return { http: r.status, body: await r.json().catch(() => ({})) };
}

async function deployRules(source) {
  const c = await rulesCall('POST', `projects/${PROJECT}/rulesets`, { source: { files: [{ name: 'firestore.rules', content: source }] } });
  if (c.http !== 200) throw new Error(`create ruleset: HTTP ${c.http} ${JSON.stringify(c.body).slice(0, 800)}`);
  const name = c.body.name;
  const u = await rulesCall('PATCH', `projects/${PROJECT}/releases/cloud.firestore`, { release: { name: `projects/${PROJECT}/releases/cloud.firestore`, rulesetName: name } });
  if (u.http !== 200) throw new Error(`update release: HTTP ${u.http} ${JSON.stringify(u.body).slice(0, 400)}`);
  return name;
}

// ---- operations ----

const tsv = (ms) => new Date(ms).toISOString();
const ts = (iso) => ({ timestampValue: iso });

// gv converts a REST value tree to what proto-loader expects.
function gv(x, key) {
  if (Buffer.isBuffer(x)) return x;
  if (Array.isArray(x)) return x.map((y) => gv(y));
  if (x && typeof x === 'object') {
    return Object.fromEntries(Object.entries(x).map(([k, y]) => {
      if (k === 'nullValue') return [k, 'NULL_VALUE'];
      if (k === 'timestampValue' && typeof y === 'string') {
        const t = Date.parse(y);
        return [k, { seconds: String(Math.floor(t / 1000)), nanos: (t % 1000) * 1_000_000 }];
      }
      if (k === 'limit' && typeof y === 'number') return [k, { value: y }];
      return [k, gv(y, k)];
    }));
  }
  return x;
}

const nm = (p) => `${ROOT}/${p}`;
const W = {
  set: (p, data, o = {}) => ({
    update: { name: nm(p), fields: fields(data) },
    ...(o.mask ? { updateMask: { fieldPaths: o.mask } } : {}),
    ...(o.exists !== undefined ? { currentDocument: { exists: o.exists } } : {}),
    ...(o.tr ? { updateTransforms: o.tr } : {}),
  }),
  del: (p, o = {}) => ({ delete: nm(p), ...(o.exists !== undefined ? { currentDocument: { exists: o.exists } } : {}) }),
};
const TR = {
  ts: (f) => ({ fieldPath: f, setToServerValue: 'REQUEST_TIME' }),
  inc: (f, n) => ({ fieldPath: f, increment: Number.isInteger(n) ? { integerValue: String(n) } : { doubleValue: n } }),
  max: (f, n) => ({ fieldPath: f, maximum: { integerValue: String(n) } }),
  min: (f, n) => ({ fieldPath: f, minimum: { integerValue: String(n) } }),
  union: (f, arr) => ({ fieldPath: f, appendMissingElements: { values: arr.map(v) } }),
  remove: (f, arr) => ({ fieldPath: f, removeAllFromArray: { values: arr.map(v) } }),
};
const F = (f, op, x) => ({ fieldFilter: { field: { fieldPath: f }, op, value: v(x) } });
const OR = (...fs) => ({ compositeFilter: { op: 'OR', filters: fs } });
const q = (c, o = {}) => ({
  from: [{ collectionId: c, ...(o.group ? { allDescendants: true } : {}) }],
  ...(o.where ? { where: o.where } : {}),
  ...(o.orderBy ? { orderBy: o.orderBy.map((f) => ({ field: { fieldPath: f }, direction: 'ASCENDING' })) } : {}),
  ...(o.limit !== undefined ? { limit: o.limit } : {}),
  ...(o.offset !== undefined ? { offset: o.offset } : {}),
});

// run executes one operation on a transport and returns {request, response, outcome, raw}.
// ctx.txn holds the transaction id of the run (a string over REST, a Buffer over gRPC).
async function run(transport, op, ctx) {
  const txn = op.transaction === '$t' ? ctx.txn : undefined;
  const body = (o) => JSON.parse(JSON.stringify(o));
  let method, path, rbody, rpc, greq, stream = false;
  const T_ = (b) => (txn ? { ...b, transaction: txn } : b);
  switch (op.op) {
    case 'get':
      method = 'GET';
      path = `${op.name}${txn ? `?transaction=${encodeURIComponent(txn)}` : ''}`;
      rpc = 'getDocument';
      greq = T_({ name: op.name });
      break;
    case 'list':
      method = 'GET';
      path = `${op.parent}/${op.collectionId}`;
      rpc = 'listDocuments';
      greq = { parent: op.parent, collectionId: op.collectionId };
      break;
    case 'batchGet':
      method = 'POST';
      path = `${DB}/documents:batchGet`;
      rbody = T_({ database: DB, documents: op.documents });
      rpc = 'batchGetDocuments';
      stream = true;
      greq = rbody;
      break;
    case 'runQuery':
      method = 'POST';
      path = `${op.parent}:runQuery`;
      rbody = T_({ structuredQuery: op.structuredQuery });
      rpc = 'runQuery';
      stream = true;
      greq = { parent: op.parent, ...rbody };
      break;
    case 'aggregate':
      method = 'POST';
      path = `${op.parent}:runAggregationQuery`;
      rbody = { structuredAggregationQuery: { structuredQuery: op.structuredQuery, aggregations: [{ alias: 'n', count: {} }] } };
      rpc = 'runAggregationQuery';
      stream = true;
      greq = { parent: op.parent, ...rbody };
      break;
    case 'commit':
      method = 'POST';
      path = `${DB}/documents:commit`;
      rbody = T_({ writes: op.writes });
      rpc = 'commit';
      greq = { database: DB, ...rbody };
      break;
    case 'batchWrite':
      method = 'POST';
      path = `${DB}/documents:batchWrite`;
      rbody = { writes: op.writes };
      rpc = 'batchWrite';
      greq = { database: DB, ...rbody };
      break;
    case 'create':
      method = 'POST';
      path = `${op.parent}/${op.collectionId}?documentId=${encodeURIComponent(op.documentId)}`;
      rbody = { fields: fields(op.data) };
      rpc = 'createDocument';
      greq = { parent: op.parent, collectionId: op.collectionId, documentId: op.documentId, document: rbody };
      break;
    case 'patch': {
      const qs = [];
      for (const f of op.mask || []) qs.push(`updateMask.fieldPaths=${encodeURIComponent(f)}`);
      if (op.exists !== undefined) qs.push(`currentDocument.exists=${op.exists}`);
      method = 'PATCH';
      path = `${op.name}${qs.length ? `?${qs.join('&')}` : ''}`;
      rbody = { fields: fields(op.data) };
      rpc = 'updateDocument';
      greq = { document: { name: op.name, fields: rbody.fields }, ...(op.mask ? { updateMask: { fieldPaths: op.mask } } : {}), ...(op.exists !== undefined ? { currentDocument: { exists: op.exists } } : {}) };
      break;
    }
    case 'delete':
      method = 'DELETE';
      path = `${op.name}${op.exists !== undefined ? `?currentDocument.exists=${op.exists}` : ''}`;
      rpc = 'deleteDocument';
      greq = { name: op.name, ...(op.exists !== undefined ? { currentDocument: { exists: op.exists } } : {}) };
      break;
    case 'begin':
      method = 'POST';
      path = `${DB}/documents:beginTransaction`;
      rbody = op.options ? { options: op.options } : {};
      rpc = 'beginTransaction';
      greq = { database: DB, ...rbody };
      break;
    case 'rollback':
      method = 'POST';
      path = `${DB}/documents:rollback`;
      rbody = { transaction: txn };
      rpc = 'rollback';
      greq = { database: DB, transaction: txn };
      break;
    default:
      throw new Error(`unknown op ${op.op}`);
  }
  const replace = new Map();
  const noteTxn = () => {
    if (ctx.txn) replace.set(Buffer.isBuffer(ctx.txn) ? ctx.txn.toString('base64') : ctx.txn, '<transaction>');
  };
  noteTxn();
  const out = { request: transport === 'rest' ? { method, path: path.replace(ctx.txn && !Buffer.isBuffer(ctx.txn) ? encodeURIComponent(ctx.txn) : '\0', '<transaction>'), ...(rbody ? { body: rbody } : {}) } : { rpc: `google.firestore.v1.Firestore/${rpc[0].toUpperCase()}${rpc.slice(1)}`, message: greq } };
  let error, resp;
  if (transport === 'rest') {
    const r = await rest(method, path, rbody);
    if (r.http === 429) throw new Error('HTTP 429 from Firestore: stopping, as agreed');
    resp = { http: r.http, bodyIsArray: Array.isArray(r.body), body: r.body };
    const arr = Array.isArray(r.body) ? r.body : [r.body];
    error = arr.find((x) => x && x.error)?.error;
    if (op.op === 'begin' && r.body.transaction) ctx.newTxn = r.body.transaction;
    if (error) error = { http: r.http, status: error.status, message: error.message };
  } else {
    const g = gv(greq);
    const r = stream ? await grpcStream(rpc, g) : await grpcUnary(rpc, g);
    if (r.error?.code === 8 && /quota|exhausted/i.test(r.error.message || '')) throw new Error('RESOURCE_EXHAUSTED from Firestore: stopping, as agreed');
    resp = r.ok ? { ok: true, [stream ? 'responses' : 'message']: stream ? r.responses : r.response } : { ok: false, ...(stream && r.responses.length ? { responses: r.responses } : {}) };
    if (!r.ok) error = { code: r.error.code, status: r.error.status, message: r.error.message };
    if (op.op === 'begin' && r.ok) ctx.newTxn = r.response.transaction;
  }
  if (ctx.newTxn) {
    replace.set(Buffer.isBuffer(ctx.newTxn) ? ctx.newTxn.toString('base64') : ctx.newTxn, '<transaction>');
  }
  const clean = (x) => JSON.parse(relProject(JSON.stringify(normalise(x, replace))));
  out.request = clean(compact(out.request));
  out.response = clean(resp);
  if (error) out.error = clean(error);
  out.outcome = error ? error.status || String(error.code) : 'OK';
  return out;
}

// ---- cases ----

const written = new Set(); // every document path the recorder may have created
let calls = 0;

async function admin(writes) {
  for (const w of writes) {
    const n = w.update?.name ?? w.delete;
    assertScoped(n);
    written.add(n);
  }
  await as('owner');
  const r = await rest('POST', `${DB}/documents:commit`, { writes });
  calls++;
  if (r.http === 429) throw new Error('HTTP 429 from Firestore: stopping, as agreed');
  if (r.http !== 200) throw new Error(`admin commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
}

async function readBack(paths) {
  await as('owner');
  const r = await rest('POST', `${DB}/documents:batchGet`, { documents: paths.map(nm) });
  calls++;
  if (r.http !== 200) throw new Error(`readback failed: HTTP ${r.http}`);
  const out = {};
  for (const x of r.body) {
    if (x.found) out[rel(x.found.name)] = normalise(x.found.fields);
    else if (x.missing) out[rel(x.missing)] = null;
  }
  return Object.fromEntries(Object.keys(out).sort().map((k) => [k, out[k]]));
}

const files = {}; // topic -> cases
const globalSeed = {};

async function reset(c) {
  const seedPaths = Object.keys(c.seed || {});
  const touch = [...new Set([...(c.touch || []), ...seedPaths])];
  if (!touch.length) return;
  const writes = [];
  for (const p of touch) writes.push(c.seed?.[p] ? W.set(p, c.seed[p]) : W.del(p));
  await admin(writes);
}

async function runCase(topic, c) {
  log(`${c.id} `);
  const rec = {
    id: c.id,
    description: c.description,
    actor: c.actor || 'user',
    ...(c.seed && Object.keys(c.seed).length ? { seed: c.seed } : {}),
    transports: {},
  };
  for (const tr of c.transports || ['rest', 'grpc']) {
    await reset(c);
    await as(c.actor || 'user');
    const ctx = {};
    const steps = [];
    for (const op of c.ops) {
      if (typeof op === 'function') {
        await op(tr, ctx);
        continue;
      }
      const s = await run(tr, op, ctx);
      steps.push(s);
      if (op.op === 'begin') ctx.txn = ctx.newTxn;
      calls++;
      await sleep(60);
    }
    const t = { steps };
    t.outcome = steps[steps.length - 1].outcome;
    const touched = [...(c.touch || [])];
    if (touched.length) t.after = await readBack(touched);
    rec.transports[tr] = t;
  }
  const o = Object.values(rec.transports).map((t) => t.outcome);
  rec.outcome = Object.fromEntries(Object.entries(rec.transports).map(([k, t]) => [k, t.outcome]));
  rec.transportsAgree = new Set(o).size === 1;
  (files[topic] ||= []).push(rec);
  log(`${Object.values(rec.outcome).join('/')}\n`);
  return rec;
}

// helpers to build the cases
const g = (c, id, desc, actor, extra = {}) => ({ id, description: desc, actor, ops: [{ op: 'get', name: nm(`${c}/${id.split(':')[1] || ''}`) }], ...extra });
const doc = (c, d) => `${c}/${d}`;
const ROOTOP = { parent: ROOT };

function buildCases(users) {
  const m1 = col('m1'), m2 = col('m2'), m3 = col('m3'), m4 = col('m4'), m5 = col('m5'), m6 = col('m6'), m7 = col('m7'), m8 = col('m8'), m9 = col('m9');
  const cases = { methods: [], auth: [], request: [], lookups: [], queries: [], batches: [] };
  const add = (topic, id, description, c) => cases[topic].push({ id, description, ...c });

  // -- 1. methods --
  const E = { a: 1, b: 1, m: { x: 1, y: 2 }, arr: [1], n: 5 };
  const one = (c, d) => ({ seed: { [doc(c, d)]: { a: 1 } }, touch: [doc(c, d)] });
  for (const [col_, label] of [[m1, 'read-write']]) {
    add('methods', `${label}.get-allowed`, 'get of a document the rule allows (allow read, write)', { ...one(col_, 'ok-1'), ops: [{ op: 'get', name: nm(doc(col_, 'ok-1')) }] });
    add('methods', `${label}.get-denied`, 'get of a document the rule denies', { ...one(col_, 'bad-1'), ops: [{ op: 'get', name: nm(doc(col_, 'bad-1')) }] });
    add('methods', `${label}.get-denied-missing`, 'get of a missing document whose rule denies it', { touch: [doc(col_, 'bad-none')], ops: [{ op: 'get', name: nm(doc(col_, 'bad-none')) }] });
    add('methods', `${label}.get-allowed-missing`, 'get of a missing document whose rule allows it', { touch: [doc(col_, 'ok-none')], ops: [{ op: 'get', name: nm(doc(col_, 'ok-none')) }] });
    add('methods', `${label}.list-documents`, 'listDocuments of a collection whose rule depends on the document id', { ...one(col_, 'ok-1'), ops: [{ op: 'list', parent: ROOT, collectionId: col_ }] });
    add('methods', `${label}.run-query`, 'runQuery over a collection whose rule depends on the document id', { ...one(col_, 'ok-1'), ops: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(col_) }] });
    add('methods', `${label}.batch-get-allowed`, 'batchGet of two documents the rule allows', { seed: { [doc(col_, 'ok-1')]: { a: 1 }, [doc(col_, 'ok-2')]: { a: 2 } }, ops: [{ op: 'batchGet', documents: [nm(doc(col_, 'ok-1')), nm(doc(col_, 'ok-2'))] }] });
    add('methods', `${label}.batch-get-one-denied`, 'batchGet of one allowed and one denied document', { seed: { [doc(col_, 'ok-1')]: { a: 1 }, [doc(col_, 'bad-1')]: { a: 2 } }, ops: [{ op: 'batchGet', documents: [nm(doc(col_, 'ok-1')), nm(doc(col_, 'bad-1'))] }] });
    add('methods', `${label}.create-allowed`, 'createDocument the rule allows', { touch: [doc(col_, 'ok-new')], ops: [{ op: 'create', parent: ROOT, collectionId: col_, documentId: 'ok-new', data: { a: 1 } }] });
    add('methods', `${label}.create-denied`, 'createDocument the rule denies', { touch: [doc(col_, 'bad-new')], ops: [{ op: 'create', parent: ROOT, collectionId: col_, documentId: 'bad-new', data: { a: 1 } }] });
    add('methods', `${label}.commit-set-allowed`, 'commit of a write that creates a document the rule allows', { touch: [doc(col_, 'ok-set')], ops: [{ op: 'commit', writes: [W.set(doc(col_, 'ok-set'), { a: 1 })] }] });
    add('methods', `${label}.commit-set-denied`, 'commit of a write that creates a document the rule denies', { touch: [doc(col_, 'bad-set')], ops: [{ op: 'commit', writes: [W.set(doc(col_, 'bad-set'), { a: 1 })] }] });
    add('methods', `${label}.patch-allowed`, 'updateDocument of an existing document the rule allows', { ...one(col_, 'ok-1'), ops: [{ op: 'patch', name: nm(doc(col_, 'ok-1')), data: { a: 2 }, mask: ['a'] }] });
    add('methods', `${label}.patch-denied`, 'updateDocument of an existing document the rule denies', { ...one(col_, 'bad-1'), ops: [{ op: 'patch', name: nm(doc(col_, 'bad-1')), data: { a: 2 }, mask: ['a'] }] });
    add('methods', `${label}.delete-allowed`, 'deleteDocument the rule allows', { ...one(col_, 'ok-1'), ops: [{ op: 'delete', name: nm(doc(col_, 'ok-1')) }] });
    add('methods', `${label}.delete-denied`, 'deleteDocument the rule denies', { ...one(col_, 'bad-1'), ops: [{ op: 'delete', name: nm(doc(col_, 'bad-1')) }] });
    add('methods', `${label}.delete-denied-missing`, 'deleteDocument of a missing document whose rule denies delete', { touch: [doc(col_, 'bad-none')], ops: [{ op: 'delete', name: nm(doc(col_, 'bad-none')) }] });
    add('methods', `${label}.delete-allowed-missing`, 'deleteDocument of a missing document whose rule allows delete', { touch: [doc(col_, 'ok-none')], ops: [{ op: 'delete', name: nm(doc(col_, 'ok-none')) }] });
  }
  // split methods A: get, create, delete allowed on ok-*
  const sp = [
    [m2, 'split-gcd', 'get/create/delete allowed, no list or update rule', [['get', 'ok-1'], ['list'], ['create', 'ok-new'], ['update', 'ok-1'], ['delete', 'ok-1']]],
    [m3, 'split-lu', 'list/update allowed, no get/create/delete rule', [['get', 'ok-1'], ['list'], ['create', 'ok-new'], ['update', 'ok-1'], ['delete', 'ok-1']]],
  ];
  for (const [c, label, desc, ms] of sp) {
    for (const [m, d] of ms) {
      const base = { ...(m === 'list' ? one(c, 'ok-1') : d && m !== 'create' ? one(c, d) : {}), ...(m === 'create' ? { touch: [doc(c, d)] } : {}) };
      const ops = {
        get: [{ op: 'get', name: nm(doc(c, d)) }],
        list: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(c) }],
        create: [{ op: 'create', parent: ROOT, collectionId: c, documentId: d, data: { a: 1 } }],
        update: [{ op: 'patch', name: nm(doc(c, d)), data: { a: 2 }, mask: ['a'] }],
        delete: [{ op: 'delete', name: nm(doc(c, d)) }],
      }[m];
      add('methods', `${label}.${m}`, `${desc}: ${m}`, { ...base, ops });
    }
  }
  // request.method values
  for (const [m, d, op] of [
    ['get', 'get', (c) => [{ op: 'get', name: nm(doc(c, 'get')) }]],
    ['get-wrong-name', 'list', (c) => [{ op: 'get', name: nm(doc(c, 'list')) }]],
    ['batch-get', 'get', (c) => [{ op: 'batchGet', documents: [nm(doc(c, 'get'))] }]],
    ['delete', 'delete', (c) => [{ op: 'delete', name: nm(doc(c, 'delete')) }]],
    ['update', 'update', (c) => [{ op: 'patch', name: nm(doc(c, 'update')), data: { a: 2 }, mask: ['a'] }]],
    ['update-no-mask', 'update', (c) => [{ op: 'patch', name: nm(doc(c, 'update')), data: { a: 2 } }]],
    ['create-via-create', 'create', (c) => [{ op: 'create', parent: ROOT, collectionId: c, documentId: 'create', data: { a: 1 } }]],
    ['create-via-commit-set', 'create', (c) => [{ op: 'commit', writes: [W.set(doc(c, 'create'), { a: 1 })] }]],
    ['set-existing-is-update', 'update', (c) => [{ op: 'commit', writes: [W.set(doc(c, 'update'), { a: 1 })] }]],
    ['set-missing-named-update', 'update', (c) => [{ op: 'commit', writes: [W.set(doc(c, 'update'), { a: 1 })] }]],
    ['list-run-query', null, (c) => [{ op: 'runQuery', parent: ROOT, structuredQuery: q(c) }]],
    ['list-documents', null, (c) => [{ op: 'list', parent: ROOT, collectionId: c }]],
    ['count', null, (c) => [{ op: 'aggregate', parent: ROOT, structuredQuery: q(c) }]],
  ]) {
    const seeded = ['get', 'get-wrong-name', 'batch-get', 'delete', 'update', 'update-no-mask', 'set-existing-is-update', 'list-run-query', 'list-documents', 'count'].includes(m);
    const sd = m === 'get-wrong-name' ? 'list' : d || 'x';
    add('methods', `request-method.${m}`, `request.method under 'allow get/create/update/delete: if request.method == <doc id>' and 'allow list: if request.method == "list"': ${m}`, {
      seed: seeded ? { [doc(m4, sd)]: { a: 1 } } : {},
      touch: [doc(m4, d || 'x'), doc(m4, sd)],
      ops: op(m4),
    });
  }
  // preconditions against rules
  add('methods', 'order.update-existing-create-only', 'update (no precondition) of an existing document where only create is allowed', { ...one(m5, 'e'), ops: [{ op: 'patch', name: nm(doc(m5, 'e')), data: { a: 2 }, mask: ['a'] }] });
  add('methods', 'order.create-missing-create-only', 'update (no precondition) of a missing document where only create is allowed: it is a create', { touch: [doc(m5, 'e')], ops: [{ op: 'patch', name: nm(doc(m5, 'e')), data: { a: 2 }, mask: ['a'] }] });
  add('methods', 'order.create-existing-create-only', 'create (exists:false) of an existing document where only create is allowed', { ...one(m5, 'e'), ops: [{ op: 'commit', writes: [W.set(doc(m5, 'e'), { a: 2 }, { exists: false })] }] });
  add('methods', 'order.create-document-existing-create-only', 'createDocument with an existing id where only create is allowed', { ...one(m5, 'e'), ops: [{ op: 'create', parent: ROOT, collectionId: m5, documentId: 'e', data: { a: 2 } }] });
  add('methods', 'order.create-existing-nothing-allowed', 'create (exists:false) of an existing document where no write is allowed', { ...one(m6, 'e'), ops: [{ op: 'commit', writes: [W.set(doc(m6, 'e'), { a: 2 }, { exists: false })] }] });
  add('methods', 'order.create-document-existing-nothing-allowed', 'createDocument with an existing id where no write is allowed', { ...one(m6, 'e'), ops: [{ op: 'create', parent: ROOT, collectionId: m6, documentId: 'e', data: { a: 2 } }] });
  add('methods', 'order.update-missing-precondition-nothing-allowed', 'update with exists:true of a missing document where no write is allowed', { touch: [doc(m6, 'e')], ops: [{ op: 'commit', writes: [W.set(doc(m6, 'e'), { a: 2 }, { exists: true, mask: ['a'] })] }] });
  add('methods', 'order.update-missing-precondition-update-only', 'update with exists:true of a missing document where only update is allowed', { touch: [doc(m9, 'e')], ops: [{ op: 'commit', writes: [W.set(doc(m9, 'e'), { a: 2 }, { exists: true, mask: ['a'] })] }] });
  add('methods', 'order.update-existing-exists-false-update-only', 'create (exists:false) of an existing document where only update is allowed', { ...one(m9, 'e'), ops: [{ op: 'commit', writes: [W.set(doc(m9, 'e'), { a: 2 }, { exists: false })] }] });
  add('methods', 'order.delete-missing-nothing-allowed', 'delete of a missing document where no write is allowed', { touch: [doc(m6, 'e')], ops: [{ op: 'delete', name: nm(doc(m6, 'e')) }] });
  add('methods', 'order.delete-missing-precondition-nothing-allowed', 'delete with exists:true of a missing document where no write is allowed', { touch: [doc(m6, 'e')], ops: [{ op: 'delete', name: nm(doc(m6, 'e')), exists: true }] });
  add('methods', 'order.get-missing-rule-reads-data', 'get of a missing document under a rule that reads resource.data', { touch: [doc(m7, 'e')], ops: [{ op: 'get', name: nm(doc(m7, 'e')) }] });
  add('methods', 'order.get-existing-rule-reads-data-true', 'get of an existing document whose data satisfies the rule', { seed: { [doc(m7, 'e')]: { x: 1 } }, touch: [doc(m7, 'e')], ops: [{ op: 'get', name: nm(doc(m7, 'e')) }] });
  add('methods', 'order.get-existing-rule-reads-data-false', 'get of an existing document whose data fails the rule', { seed: { [doc(m7, 'e')]: { x: 2 } }, touch: [doc(m7, 'e')], ops: [{ op: 'get', name: nm(doc(m7, 'e')) }] });
  add('methods', 'order.get-missing-rule-allows-all', 'get of a missing document under an allow-all get rule', { touch: [doc(m8, 'e')], ops: [{ op: 'get', name: nm(doc(m8, 'e')) }] });
  add('methods', 'no-match.get', 'get under no match block at all', { touch: [`${P}-nomatch/e`], seed: { [`${P}-nomatch/e`]: { a: 1 } }, ops: [{ op: 'get', name: nm(`${P}-nomatch/e`) }] });
  add('methods', 'no-match.commit', 'write under no match block at all', { touch: [`${P}-nomatch/e`], ops: [{ op: 'commit', writes: [W.set(`${P}-nomatch/e`, { a: 1 })] }] });

  // -- 2. auth --
  const A = col('a');
  const authIds = [
    'authnull', 'authnotnull', 'uid-a', 'uid-b', 'uid-is-string', 'email', 'verified-false', 'verified-true', 'verified-bool', 'prov-password', 'prov-anonymous', 'ident-email',
    'sub-is-uid', 'aud', 'iss', 'auth-time-int', 'iat-int', 'exp-int', 'firebase-map', 'token-map', 'auth-keys', 'auth-keys-only',
    ...['name', 'email', 'email_verified', 'sub', 'iss', 'aud', 'iat', 'exp', 'auth_time', 'user_id', 'firebase', 'picture', 'phone_number', 'admin', 'uid', 'nonce', 'azp'].map((k) => `tok_${k}`),
    ...['sign_in_provider', 'identities', 'tenant', 'sign_in_second_factor', 'second_factor_identifier'].map((k) => `fb_${k}`),
  ];
  const sub = (ids) => authIds.filter((i) => ids.includes(i) || i.startsWith('tok_') || i.startsWith('fb_'));
  const byActor = {
    user: authIds,
    userB: ['authnull', 'authnotnull', 'uid-a', 'uid-b', 'email'],
    anon: sub(['authnull', 'authnotnull', 'uid-a', 'uid-is-string', 'email', 'verified-false', 'verified-true', 'prov-password', 'prov-anonymous', 'sub-is-uid', 'auth-keys', 'auth-keys-only']),
    none: ['authnull', 'authnotnull', 'uid-a', 'email', 'tok_name', 'tok_sub', 'auth-keys'],
  };
  for (const [actor, ids] of Object.entries(byActor)) {
    for (const id of ids) {
      add('auth', `${actor}.${id}`, `get of ${A}/${id} by ${actor}: the rule asserts '${id}' about request.auth`, { actor, ops: [{ op: 'get', name: nm(doc(A, id)) }] });
    }
  }
  for (const id of ['authnull', 'uid-a']) {
    add('auth', `none.batch-get-${id}`, `unauthenticated batchGet of ${id}`, { actor: 'none', ops: [{ op: 'batchGet', documents: [nm(doc(A, id))] }] });
  }
  add('auth', 'none.run-query', 'unauthenticated runQuery where only get is allowed', { actor: 'none', ops: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(A) }] });
  add('auth', 'none.write-denied', 'unauthenticated write where no write is allowed', { actor: 'none', touch: [doc(m6, 'e')], ops: [{ op: 'commit', writes: [W.set(doc(m6, 'e'), { a: 1 })] }] });

  // -- 3 and 4. request.resource, resource, time, path --
  const R = col('r');
  const T_ = col('t');
  const upd = (id, ops, desc, seed = E) => add('request', `rr.${id}`, desc, { seed: { [doc(R, id)]: seed }, touch: [doc(R, id)], ops });
  const set = (id, data, o) => [{ op: 'commit', writes: [W.set(doc(R, id), data, o)] }];
  upd('u-mask-merge', [{ op: 'patch', name: nm(doc(R, 'u-mask-merge')), data: { a: 2 }, mask: ['a'] }], 'update with a mask: request.resource.data keeps the fields the mask leaves out');
  upd('u-res-intact', [{ op: 'patch', name: nm(doc(R, 'u-res-intact')), data: { a: 2 }, mask: ['a'] }], 'update with a mask: resource.data is the stored document');
  upd('u-mask-keys', [{ op: 'patch', name: nm(doc(R, 'u-mask-keys')), data: { a: 2 }, mask: ['a'] }], 'update with a mask: the keys of request.resource.data are all the merged ones');
  upd('u-mask-affected', [{ op: 'patch', name: nm(doc(R, 'u-mask-affected')), data: { a: 2 }, mask: ['a'] }], 'update with a mask: diff().affectedKeys() is the changed field only');
  upd('u-mask-delete', [{ op: 'patch', name: nm(doc(R, 'u-mask-delete')), data: {}, mask: ['b'] }], 'update whose mask names a field the document of the request lacks: that field is deleted in request.resource.data');
  upd('u-mask-nested', [{ op: 'patch', name: nm(doc(R, 'u-mask-nested')), data: { m: { x: 9 } }, mask: ['m.x'] }], 'update with a nested mask path merges into the map');
  upd('u-nomask', [{ op: 'patch', name: nm(doc(R, 'u-nomask')), data: { a: 2 } }], 'update without a mask replaces the document: request.resource.data is the request fields only');
  const tsOnly = (id) => set(id, {}, { mask: [], tr: [TR.ts('t')] });
  upd('u-ts-eq-time', tsOnly('u-ts-eq-time'), 'update with only a serverTimestamp transform: request.resource.data.t equals request.time');
  upd('u-ts-type', tsOnly('u-ts-type'), 'update with a serverTimestamp transform: request.resource.data.t is a timestamp');
  upd('u-ts-absent', tsOnly('u-ts-absent'), 'update with a serverTimestamp transform: the transformed field is absent from request.resource.data');
  upd('u-ts-with-field', set('u-ts-with-field', { a: 2 }, { mask: ['a'], tr: [TR.ts('t')] }), 'update with a field and a serverTimestamp transform');
  upd('u-ts-keys', tsOnly('u-ts-keys'), 'update with a serverTimestamp transform: the merged keys include the transformed field');
  const inc = (id, n = 3) => set(id, {}, { mask: [], tr: [TR.inc('n', n)] });
  upd('u-inc-8', inc('u-inc-8'), 'increment 3 on n=5: request.resource.data.n is 8');
  upd('u-inc-5', inc('u-inc-5'), 'increment 3 on n=5: request.resource.data.n is still 5');
  upd('u-inc-3', inc('u-inc-3'), 'increment 3 on n=5: request.resource.data.n is the operand 3');
  upd('u-inc-absent', inc('u-inc-absent'), 'increment 3 on n=5: n is absent from request.resource.data');
  upd('u-inc-int', inc('u-inc-int'), 'increment 3 on n=5: n is an int');
  upd('u-inc-double', inc('u-inc-double', 0.5), 'increment 0.5 on n=5: request.resource.data.n is 5.5');
  upd('u-inc-diff', inc('u-inc-diff'), 'increment 3 on n=5: diff().affectedKeys() is [n]');
  upd('u-max-9', set('u-max-9', {}, { mask: [], tr: [TR.max('n', 9)] }), 'maximum 9 on n=5: n is 9');
  upd('u-max-5', set('u-max-5', {}, { mask: [], tr: [TR.max('n', 9)] }), 'maximum 9 on n=5: n is still 5');
  upd('u-min-2', set('u-min-2', {}, { mask: [], tr: [TR.min('n', 2)] }), 'minimum 2 on n=5: n is 2');
  upd('u-au-merged', set('u-au-merged', {}, { mask: [], tr: [TR.union('arr', [2])] }), 'appendMissingElements [2] on arr=[1]: arr is [1,2]');
  upd('u-au-sent', set('u-au-sent', {}, { mask: [], tr: [TR.union('arr', [2])] }), 'appendMissingElements [2] on arr=[1]: arr is the operand [2]');
  upd('u-au-before', set('u-au-before', {}, { mask: [], tr: [TR.union('arr', [2])] }), 'appendMissingElements [2] on arr=[1]: arr is still [1]');
  upd('u-ra-removed', set('u-ra-removed', {}, { mask: [], tr: [TR.remove('arr', [1])] }), 'removeAllFromArray [1] on arr=[1]: arr is []');
  const cr = (id, data, tr, desc) => add('request', `rr.${id}`, desc, { touch: [doc(R, id)], ops: set(id, data, tr ? { tr } : undefined) });
  cr('c-res-null', { a: 1 }, null, 'create: resource is null');
  cr('c-data-eq', { a: 1 }, null, 'create: request.resource.data is the request fields');
  cr('c-keys', { a: 1 }, null, 'create: request.resource.data keys');
  cr('c-ts-eq-time', { a: 1 }, [TR.ts('t')], 'create with a serverTimestamp transform: t equals request.time');
  cr('c-ts-type', { a: 1 }, [TR.ts('t')], 'create with a serverTimestamp transform: t is a timestamp');
  cr('c-inc-3', { a: 1 }, [TR.inc('n', 3)], 'create with increment 3 on a missing field: n is 3');
  cr('c-inc-absent', { a: 1 }, [TR.inc('n', 3)], 'create with increment 3 on a missing field: n is absent');
  cr('c-nested-ts', { a: 1, m: { x: 1 } }, [TR.ts('m.t')], 'create with a serverTimestamp transform on a nested field');
  cr('c-keys-time-first', { a: 1 }, [TR.ts('t')], 'create with a serverTimestamp transform: the keys are a and t');
  add('request', 'rr.c-req-res-id', 'create: request.resource.id is the document id', { touch: [doc(R, 'c-req-res-id')], ops: set('c-req-res-id', { a: 1 }) });
  for (const id of ['d-reqres-null', 'd-res-a', 'd-res-id', 'd-res-name', 'd-path']) {
    add('request', `rr.${id}`, `delete: ${id}`, { seed: { [doc(R, id)]: E }, touch: [doc(R, id)], ops: [{ op: 'delete', name: nm(doc(R, id)) }] });
  }
  add('request', 'rr.d-res-a-via-commit', 'delete through commit: resource.data.a is 1', { seed: { [doc(R, 'd-res-a')]: E }, touch: [doc(R, 'd-res-a')], ops: [{ op: 'commit', writes: [W.del(doc(R, 'd-res-a'))] }] });
  add('request', 'rr.d-missing', 'delete of a missing document under a rule that reads resource.data', { touch: [doc(R, 'd-res-a')], ops: [{ op: 'delete', name: nm(doc(R, 'd-res-a')) }] });
  add('request', 'rr.create-existing-update-rule', 'create (exists:false) of an existing document under "allow create, update": the rule sees an update', { seed: { [doc(R, 'u-mask-merge')]: E }, touch: [doc(R, 'u-mask-merge')], ops: [{ op: 'commit', writes: [W.set(doc(R, 'u-mask-merge'), { a: 2, b: 1 }, { exists: false })] }] });
  add('request', 'rr.create-existing-res-null', 'create (exists:false) of an existing document: is resource null', { seed: { [doc(R, 'c-res-null')]: E }, touch: [doc(R, 'c-res-null')], ops: [{ op: 'commit', writes: [W.set(doc(R, 'c-res-null'), { a: 1 }, { exists: false })] }] });
  add('request', 'rr.set-existing-res-null', 'set (no precondition) of an existing document: is resource null', { seed: { [doc(R, 'c-res-null')]: E }, touch: [doc(R, 'c-res-null')], ops: [{ op: 'commit', writes: [W.set(doc(R, 'c-res-null'), { a: 1 })] }] });
  add('request', 'rr.create-document-existing-res-null', 'createDocument over an existing id: is resource null', { seed: { [doc(R, 'c-res-null')]: E }, touch: [doc(R, 'c-res-null')], ops: [{ op: 'create', parent: ROOT, collectionId: R, documentId: 'c-res-null', data: { a: 1 } }] });
  // time and path
  const tt = (id, ops, desc, seed) => add('request', `t.${id}`, desc, { ...(seed ? { seed: { [doc(T_, id)]: { a: 1 } } } : {}), touch: [doc(T_, id)], ops });
  for (const id of ['path', 'name', 'id', 'id-data', 'time-get', 'time-type', 'path-size', 'path-slice']) tt(id, [{ op: 'get', name: nm(doc(T_, id)) }], `get: ${id}`, true);
  add('request', 't.path-list', 'list (runQuery): request.path is the collection', { seed: { [doc(T_, 'x')]: { a: 1 } }, touch: [doc(T_, 'x')], ops: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(T_) }] });
  add('request', 't.path-list-documents', 'listDocuments: request.path is the collection', { seed: { [doc(T_, 'x')]: { a: 1 } }, touch: [doc(T_, 'x')], ops: [{ op: 'list', parent: ROOT, collectionId: T_ }] });
  add('request', 't.path-batch-get', 'batchGet: request.path is the document', { seed: { [doc(T_, 'path')]: { a: 1 } }, ops: [{ op: 'batchGet', documents: [nm(doc(T_, 'path'))] }] });
  tt('c-path', set2(T_, 'c-path', { a: 1 }), 'create: request.path is the document');
  tt('c-req-name', set2(T_, 'c-req-name', { a: 1 }), 'create: request.resource.__name__ is the document name');
  tt('c-req-id', set2(T_, 'c-req-id', { a: 1 }), 'create: request.resource.id is the document id');
  tt('c-time', set2(T_, 'c-time', { a: 1 }), 'create: request.time is a plausible timestamp');
  tt('c-time-year', set2(T_, 'c-time-year', { a: 1 }), 'create: request.time.year()');
  add('request', 't.c-clock', 'create: request.time is within two minutes of the client clock', { touch: [doc(T_, 'c-clock')], ops: [(tr, ctx) => {}, { op: 'commit', writes: [W.set(doc(T_, 'c-clock'), { c: 0 })] }].slice(1), clockField: true });
  for (const id of ['u-path', 'u-res-name', 'u-req-name']) tt(id, [{ op: 'patch', name: nm(doc(T_, id)), data: { a: 2 }, mask: ['a'] }], `update: ${id}`, true);
  for (const id of ['d-path', 'd-res-name']) tt(id, [{ op: 'delete', name: nm(doc(T_, id)) }], `delete: ${id}`, true);

  // -- 5. lookups --
  const L1 = col('l1'), L2c = col('l2'), L2T = col('l2t');
  for (const id of ['lk-get', 'lk-get-val', 'lk-get-id', 'lk-get-name', 'lk-exists-true', 'lk-exists-false', 'lk-get-missing', 'lk-get-missing-or', 'lk-get-missing-not', 'lk-exists-guard', 'lk-ga', 'lk-ea', 'lk-ea-missing', 'lk-ga-missing', 'lk-get-dyn', 'lk-get-self', 'lk-get-denied']) {
    add('lookups', `read.${id}`, `get of ${L1}/${id}: its rule asserts ${id}`, { seed: { [doc(L1, id)]: { p: 1 } }, touch: [], ops: [{ op: 'get', name: nm(doc(L1, id)) }] });
  }
  for (const id of ['g10-a', 'g11-a', 'g20-a', 'g21-a', 'e10', 'e11', 'mix11', 'same10', 'same11', 'ga10', 'ga11']) {
    add('lookups', `limit.get-${id}`, `get of one document whose rule makes lookups (${id}): single-document limit of 10`, { seed: { [doc(L1, id)]: { p: 1 } }, ops: [{ op: 'get', name: nm(doc(L1, id)) }] });
  }
  const bg = (ids) => ({ seed: Object.fromEntries(ids.map((i) => [doc(L1, i), { p: 1 }])), ops: [{ op: 'batchGet', documents: ids.map((i) => nm(doc(L1, i))) }] });
  add('lookups', 'limit.batch-get-g10x2', 'batchGet of two documents with 10 lookups each (20 in all)', bg(['g10-a', 'g10-b']));
  add('lookups', 'limit.batch-get-g10x3', 'batchGet of three documents with 10 lookups each (30 in all)', bg(['g10-a', 'g10-b', 'g10-c']));
  add('lookups', 'limit.batch-get-g11x2', 'batchGet of two documents with 11 lookups each', bg(['g11-a', 'g11-b']));
  add('lookups', 'limit.batch-get-g11x1', 'batchGet of one document with 11 lookups', bg(['g11-a']));
  add('lookups', 'limit.batch-get-g20x1', 'batchGet of one document with 20 lookups', bg(['g20-a']));
  add('lookups', 'limit.batch-get-g20x2', 'batchGet of two documents with 20 lookups each (40)', bg(['g20-a', 'g20-b']));
  add('lookups', 'limit.batch-get-g10-g11', 'batchGet of a 10-lookup and an 11-lookup document (21)', bg(['g10-a', 'g11-a']));
  add('lookups', 'limit.batch-get-g10-g10-ok-denied', 'batchGet of a 10-lookup document and a missing one', { seed: { [doc(L1, 'g10-a')]: { p: 1 } }, touch: [doc(L1, 'g10-b')], ops: [{ op: 'batchGet', documents: [nm(doc(L1, 'g10-a')), nm(doc(L1, 'g10-b'))] }] });
  for (const [c, id, desc] of [[col('l3'), 'l3', 'a list rule with 10 lookups'], [col('l4'), 'l4', 'a list rule with 11 lookups']]) {
    add('lookups', `limit.query-${id}`, `runQuery under ${desc}`, { seed: { [doc(c, 'x')]: { p: 1 } }, ops: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(c) }] });
    add('lookups', `limit.list-documents-${id}`, `listDocuments under ${desc}`, { seed: { [doc(c, 'x')]: { p: 1 } }, ops: [{ op: 'list', parent: ROOT, collectionId: c }] });
  }
  const wr = (ids) => ({ touch: ids.map((i) => doc(L2c, i)), ops: [{ op: 'commit', writes: ids.map((i) => W.set(doc(L2c, i), { a: 1 })) }] });
  for (const id of ['g10-a', 'g11-a', 'g20-a', 'g21-a']) add('lookups', `limit.write-${id}`, `a single write whose rule makes lookups (${id})`, wr([id]));
  add('lookups', 'limit.batch-g10x2', 'one commit of two writes with 10 lookups each (20 in all)', wr(['g10-a', 'g10-b']));
  add('lookups', 'limit.batch-g10x3', 'one commit of three writes with 10 lookups each (30)', wr(['g10-a', 'g10-b', 'g10-c']));
  add('lookups', 'limit.batch-g10-g11', 'one commit of a 10-lookup and an 11-lookup write (21)', wr(['g10-a', 'g11-a']));
  add('lookups', 'limit.batch-g11x1-of-two', 'one commit of an 11-lookup write and a no-lookup write', { touch: [doc(L2c, 'g11-a'), doc(L2c, 'ga-noop')], seed: { [doc(col('l2t'), 't1')]: { v: 1, n: 5 } }, ops: [{ op: 'commit', writes: [W.set(doc(L2c, 'g11-a'), { a: 1 }), W.set(doc(L2c, 'ga-noop'), { a: 1 })] }] });
  add('lookups', 'limit.batch-g20x1', 'one commit of a 20-lookup write alone', wr(['g20-a']));
  add('lookups', 'limit.batch-g20x2', 'one commit of two 20-lookup writes (40)', wr(['g20-a', 'g20-b']));
  add('lookups', 'limit.batch-g10-g10-g10-g10', 'one commit of four 10-lookup writes (40)', wr(['g10-a', 'g10-b', 'g10-c', 'g10-d']));
  add('lookups', 'limit.batch-write-g11-g10', 'batchWrite of an 11-lookup write and a 10-lookup write: each write is its own request?', { touch: [doc(L2c, 'g11-a'), doc(L2c, 'g10-a')], ops: [{ op: 'batchWrite', writes: [W.set(doc(L2c, 'g11-a'), { a: 1 }), W.set(doc(L2c, 'g10-a'), { a: 1 })] }] });
  add('lookups', 'limit.batch-write-g10x3', 'batchWrite of three 10-lookup writes (30 in all)', { touch: ['g10-a', 'g10-b', 'g10-c'].map((i) => doc(L2c, i)), ops: [{ op: 'batchWrite', writes: ['g10-a', 'g10-b', 'g10-c'].map((i) => W.set(doc(L2c, i), { a: 1 })) }] });
  add('lookups', 'limit.self-getafter', 'a write with 10 lookups plus getAfter of the document it writes (does the written document count)', { touch: [doc(L2c, 'self-ga')], ops: [{ op: 'commit', writes: [W.set(doc(L2c, 'self-ga'), { a: 1 })] }] });
  add('lookups', 'limit.self-existsafter', 'a write with 10 lookups plus existsAfter of the document it writes', { touch: [doc(L2c, 'self-ea')], ops: [{ op: 'commit', writes: [W.set(doc(L2c, 'self-ea'), { a: 1 })] }] });
  add('lookups', 'limit.self-exists', 'a write with 10 lookups plus exists of the document it writes (missing)', { touch: [doc(L2c, 'self-ex')], ops: [{ op: 'commit', writes: [W.set(doc(L2c, 'self-ex'), { a: 1 })] }] });
  add('lookups', 'limit.self-get', 'a write with 10 lookups plus get of the document it writes (existing, a is 0)', { seed: { [doc(L2c, 'self-get')]: { a: 0 } }, touch: [doc(L2c, 'self-get')], ops: [{ op: 'commit', writes: [W.set(doc(L2c, 'self-get'), { a: 1 })] }] });
  add('lookups', 'limit.peer-getafter', 'batch [g10-a (10 lookups), pk-ga (10 lookups plus existsAfter of g10-a, a document the batch writes)]: do lookups of a batch-written document count', wr(['g10-a', 'pk-ga']));
  add('lookups', 'limit.peer-exists', 'batch [g10-a (10 lookups), pk-ex (10 lookups plus exists of g10-a, written in the batch)]', wr(['g10-a', 'pk-ex']));
  add('lookups', 'limit.peer-getafter-reverse', 'batch [pk-ga, g10-a]: the same in the other order', wr(['pk-ga', 'g10-a']));
  const tgt = { v: 1, n: 5 };
  const ga = (id, writes, desc, seed = { [doc(L2T, 't1')]: tgt }, touch = [doc(L2T, 't1'), doc(L2T, 'new')]) =>
    add('lookups', `after.${id}`, desc, { seed, touch: [doc(L2c, id), ...touch], ops: [{ op: 'commit', writes }] });
  ga('ga-after', [W.set(doc(L2c, 'ga-after'), { a: 1 }), W.set(doc(L2T, 't1'), { v: 2 }, { mask: ['v'] })], 'batch [rule doc, target v=2]: getAfter sees v=2, get sees v=1');
  ga('ga-after', [W.set(doc(L2T, 't1'), { v: 2 }, { mask: ['v'] }), W.set(doc(L2c, 'ga-after'), { a: 1 })], 'batch [target v=2, rule doc]: the same in the other order');
  cases.lookups[cases.lookups.length - 1].id = 'after.ga-after-reversed';
  ga('ga-noop', [W.set(doc(L2c, 'ga-noop'), { a: 1 })], 'a write whose batch does not touch the target: getAfter equals get');
  ga('ga-create', [W.set(doc(L2c, 'ga-create'), { a: 1 }), W.set(doc(L2T, 'new'), { v: 7 })], 'batch that creates the target: existsAfter true, exists false');
  ga('ga-delete', [W.set(doc(L2c, 'ga-delete'), { a: 1 }), W.del(doc(L2T, 't1'))], 'batch that deletes the target: existsAfter false, exists true');
  ga('ga-inc', [W.set(doc(L2c, 'ga-inc'), { a: 1 }), W.set(doc(L2T, 't1'), {}, { mask: [], tr: [TR.inc('n', 3)] })], 'batch that increments the target: getAfter sees the incremented value');
  ga('ga-ts', [W.set(doc(L2c, 'ga-ts'), { a: 1 }), W.set(doc(L2T, 't1'), {}, { mask: [], tr: [TR.ts('t')] })], 'batch that sets a serverTimestamp on the target: getAfter(target).data.t equals request.time');
  ga('ga-self', [W.set(doc(L2c, 'ga-self'), { a: 1 })], 'a write of one document: getAfter of itself equals request.resource.data');
  ga('ga-peer-denied', [W.set(doc(L2c, 'ga-peer-denied'), { a: 1 }), W.set(doc(L2T, 'locked'), { v: 2 })], 'batch whose target write is denied by its own rule', undefined, [doc(L2T, 'locked')]);
  add('lookups', 'after.ga-after-batchwrite', 'batchWrite (not atomic) of [rule doc, target v=2]: does getAfter see the other write', { seed: { [doc(L2T, 't1')]: tgt }, touch: [doc(L2c, 'ga-after'), doc(L2T, 't1')], ops: [{ op: 'batchWrite', writes: [W.set(doc(L2c, 'ga-after'), { a: 1 }), W.set(doc(L2T, 't1'), { v: 2 }, { mask: ['v'] })] }] });
  add('lookups', 'after.ga-after-single-ops', 'two separate commits [target v=2] then [rule doc]: getAfter sees the committed v=2 and get also v=2 (so the rule fails)', { seed: { [doc(L2T, 't1')]: tgt }, touch: [doc(L2c, 'ga-after'), doc(L2T, 't1')], ops: [{ op: 'commit', writes: [W.set(doc(L2T, 't1'), { v: 2 }, { mask: ['v'] })] }, { op: 'commit', writes: [W.set(doc(L2c, 'ga-after'), { a: 1 })] }] });
  add('lookups', 'after.ga-tx', 'transaction: read the target, then commit [rule doc, target v=2]', { seed: { [doc(L2T, 't1')]: tgt }, touch: [doc(L2c, 'ga-tx'), doc(L2T, 't1')], ops: [{ op: 'begin' }, { op: 'batchGet', documents: [nm(doc(L2T, 't1'))], transaction: '$t' }, { op: 'commit', transaction: '$t', writes: [W.set(doc(L2c, 'ga-tx'), { a: 1 }), W.set(doc(L2T, 't1'), { v: 2 }, { mask: ['v'] })] }] });
  add('lookups', 'limit.tx-batch-g10x2', 'transaction commit of two 10-lookup writes (20)', { touch: ['g10-a', 'g10-b'].map((i) => doc(L2c, i)), ops: [{ op: 'begin' }, { op: 'commit', transaction: '$t', writes: ['g10-a', 'g10-b'].map((i) => W.set(doc(L2c, i), { a: 1 })) }] });
  add('lookups', 'limit.tx-batch-g10x3', 'transaction commit of three 10-lookup writes (30)', { touch: ['g10-a', 'g10-b', 'g10-c'].map((i) => doc(L2c, i)), ops: [{ op: 'begin' }, { op: 'commit', transaction: '$t', writes: ['g10-a', 'g10-b', 'g10-c'].map((i) => W.set(doc(L2c, i), { a: 1 })) }] });
  add('lookups', 'limit.tx-reads-g10x2', 'transaction reads of two 10-lookup documents in one batchGet (20), then a second batchGet of two more', { seed: Object.fromEntries(['g10-a', 'g10-b', 'g10-c', 'g10-d'].map((i) => [doc(L1, i), { p: 1 }])), ops: [{ op: 'begin' }, { op: 'batchGet', transaction: '$t', documents: ['g10-a', 'g10-b'].map((i) => nm(doc(L1, i))) }, { op: 'batchGet', transaction: '$t', documents: ['g10-c', 'g10-d'].map((i) => nm(doc(L1, i))) }, { op: 'rollback', transaction: '$t' }] });

  // -- 6. queries --
  const qd = (c, docs) => Object.fromEntries(Object.entries(docs).map(([k, x]) => [doc(c, k), x]));
  const Q = (id, c, docs, sq, desc, op = 'runQuery') => add('queries', id, desc, { seed: qd(c, docs), ops: [{ op, parent: ROOT, structuredQuery: sq }] });
  const q1 = col('q1'), q1d = { a: { x: 1 }, b: { x: 1 }, c: { x: 2 } };
  Q('q1.where-match', q1, q1d, q(q1, { where: F('x', 'EQUAL', 1) }), 'rule resource.data.x == 1; query where x == 1');
  Q('q1.where-other', q1, q1d, q(q1, { where: F('x', 'EQUAL', 2) }), 'rule resource.data.x == 1; query where x == 2');
  Q('q1.no-where', q1, q1d, q(q1), 'rule resource.data.x == 1; query with no filter');
  Q('q1.where-range', q1, q1d, q(q1, { where: F('x', 'GREATER_THAN', 0) }), 'rule resource.data.x == 1; query where x > 0');
  Q('q1.where-in-one', q1, q1d, q(q1, { where: F('x', 'IN', [1]) }), 'rule resource.data.x == 1; query where x in [1]');
  Q('q1.where-in-two', q1, q1d, q(q1, { where: F('x', 'IN', [1, 2]) }), 'rule resource.data.x == 1; query where x in [1, 2]');
  Q('q1.where-extra', q1, q1d, q(q1, { where: { compositeFilter: { op: 'AND', filters: [F('x', 'EQUAL', 1), F('y', 'EQUAL', 5)] } } }), 'rule resource.data.x == 1; query where x == 1 and y == 5');
  Q('q1.where-array-contains', q1, q1d, q(q1, { where: F('x', 'ARRAY_CONTAINS', 1) }), 'rule resource.data.x == 1; query where x array-contains 1');
  Q('q1.where-match-limit', q1, q1d, q(q1, { where: F('x', 'EQUAL', 1), limit: 1 }), 'rule resource.data.x == 1; query where x == 1 limit 1');
  Q('q1.where-match-offset', q1, q1d, q(q1, { where: F('x', 'EQUAL', 1), offset: 1 }), 'rule resource.data.x == 1; query where x == 1 offset 1');
  Q('q1.where-match-order', q1, q1d, q(q1, { where: F('x', 'EQUAL', 1), orderBy: ['x'] }), 'rule resource.data.x == 1; query where x == 1 order by x');
  Q('q1.where-match-order-other', q1, q1d, q(q1, { where: F('x', 'EQUAL', 1), orderBy: ['y'] }), 'rule resource.data.x == 1; query where x == 1 order by y (excludes documents without y)');
  Q('q1.count-match', q1, q1d, q(q1, { where: F('x', 'EQUAL', 1) }), 'rule resource.data.x == 1; count where x == 1', 'aggregate');
  Q('q1.count-no-where', q1, q1d, q(q1), 'rule resource.data.x == 1; count with no filter', 'aggregate');
  add('queries', 'q1.list-documents', 'rule resource.data.x == 1; listDocuments', { seed: qd(q1, q1d), ops: [{ op: 'list', parent: ROOT, collectionId: q1 }] });
  add('queries', 'q1.get-any', 'rule is list-only: get of a document', { seed: qd(q1, q1d), ops: [{ op: 'get', name: nm(doc(q1, 'a')) }] });
  Q('q1.where-match-empty-result', q1, { c: { x: 2 } }, q(q1, { where: F('x', 'EQUAL', 1) }), 'rule resource.data.x == 1; query where x == 1 that matches no document');
  Q('q1.where-other-empty-result', q1, { a: { x: 1 } }, q(q1, { where: F('x', 'EQUAL', 2) }), 'rule resource.data.x == 1; query where x == 2 that matches no document');
  Q('q1.empty-collection-no-where', q1, {}, q(q1), 'rule resource.data.x == 1; query with no filter over an empty collection');
  const five = Object.fromEntries(['a', 'b', 'c', 'd', 'e'].map((k, i) => [k, { x: i }]));
  const qq = (id, c, o, desc) => Q(id, c, five, q(c, o), desc);
  const q2 = col('q2'), q3 = col('q3');
  qq('q2.limit-3', q2, { limit: 3 }, 'rule request.query.limit <= 3; limit 3');
  qq('q2.limit-4', q2, { limit: 4 }, 'rule request.query.limit <= 3; limit 4');
  qq('q2.limit-none', q2, {}, 'rule request.query.limit <= 3; no limit');
  qq('q2.limit-1-offset-5', q2, { limit: 1, offset: 5 }, 'rule request.query.limit <= 3; limit 1 offset 5');
  qq('q2.limit-3-where', q2, { limit: 3, where: F('x', 'EQUAL', 1) }, 'rule request.query.limit <= 3; limit 3 with a filter');
  qq('q3.offset-none', q3, {}, 'rule request.query.offset <= 1; no offset');
  qq('q3.offset-1', q3, { offset: 1 }, 'rule request.query.offset <= 1; offset 1');
  qq('q3.offset-2', q3, { offset: 2 }, 'rule request.query.offset <= 1; offset 2');
  qq('q3.offset-1-limit-2', q3, { offset: 1, limit: 2 }, 'rule request.query.offset <= 1; offset 1 limit 2');
  for (const [c, nmq] of [['q4', "'limit' in request.query"], ['q5', "'offset' in request.query"], ['q6', "'orderBy' in request.query"]]) {
    qq(`${c}.plain`, col(c), {}, `rule ${nmq}; plain query`);
    qq(`${c}.limit`, col(c), { limit: 2 }, `rule ${nmq}; limit 2`);
    qq(`${c}.offset`, col(c), { offset: 1 }, `rule ${nmq}; offset 1`);
    qq(`${c}.order`, col(c), { orderBy: ['x'] }, `rule ${nmq}; order by x`);
  }
  for (const [c, nmq] of [['q7', 'request.query.orderBy.size() == 1'], ['q8', "request.query.orderBy.x == 'ASCENDING'"], ['q13', "request.query.orderBy[0] == 'x'"]]) {
    qq(`${c}.order-x`, col(c), { orderBy: ['x'] }, `rule ${nmq}; order by x`);
    qq(`${c}.plain`, col(c), {}, `rule ${nmq}; plain query`);
  }
  qq('q12.plain', col('q12'), {}, 'rule request.query.limit == null; plain query');
  qq('q12.limit', col('q12'), { limit: 2 }, 'rule request.query.limit == null; limit 2');
  const q9 = col('q9'), q9d = { a: { x: 1 }, b: { x: 1 }, c: { x: 2 } };
  Q('q9.limit-2-match', q9, q9d, q(q9, { limit: 2, where: F('x', 'EQUAL', 1) }), 'rule limit <= 2 and x == 1; limit 2 where x == 1');
  Q('q9.limit-3-match', q9, q9d, q(q9, { limit: 3, where: F('x', 'EQUAL', 1) }), 'rule limit <= 2 and x == 1; limit 3 where x == 1');
  Q('q9.limit-2-no-where', q9, q9d, q(q9, { limit: 2 }), 'rule limit <= 2 and x == 1; limit 2 no filter');
  const q10 = col('q10'), q10d = { a: { x: 1 }, b: { x: 2 }, c: { x: 3 } };
  Q('q10.or-12', q10, q10d, q(q10, { where: OR(F('x', 'EQUAL', 1), F('x', 'EQUAL', 2)) }), 'rule x in [1,2]; or(x == 1, x == 2)');
  Q('q10.or-13', q10, q10d, q(q10, { where: OR(F('x', 'EQUAL', 1), F('x', 'EQUAL', 3)) }), 'rule x in [1,2]; or(x == 1, x == 3)');
  Q('q10.in-12', q10, q10d, q(q10, { where: F('x', 'IN', [1, 2]) }), 'rule x in [1,2]; where x in [1, 2]');
  Q('q10.in-123', q10, q10d, q(q10, { where: F('x', 'IN', [1, 2, 3]) }), 'rule x in [1,2]; where x in [1, 2, 3]');
  Q('q10.in-1', q10, q10d, q(q10, { where: F('x', 'IN', [1]) }), 'rule x in [1,2]; where x in [1]');
  Q('q10.eq-1', q10, q10d, q(q10, { where: F('x', 'EQUAL', 1) }), 'rule x in [1,2]; where x == 1');
  Q('q10.eq-3', q10, q10d, q(q10, { where: F('x', 'EQUAL', 3) }), 'rule x in [1,2]; where x == 3');
  Q('q10.range', q10, q10d, q(q10, { where: { compositeFilter: { op: 'AND', filters: [F('x', 'GREATER_THANOR_EQUAL'.replace('THANOR', 'THAN_OR'), 1), F('x', 'LESS_THAN_OR_EQUAL', 2)] } } }), 'rule x in [1,2]; where 1 <= x <= 2');
  Q('q10.not-equal', q10, q10d, q(q10, { where: F('x', 'NOT_EQUAL', 3) }), 'rule x in [1,2]; where x != 3');
  Q('q10.not-in', q10, q10d, q(q10, { where: F('x', 'NOT_IN', [3]) }), 'rule x in [1,2]; where x not-in [3]');
  Q('q10.or-nested', q10, q10d, q(q10, { where: { compositeFilter: { op: 'AND', filters: [OR(F('x', 'EQUAL', 1), F('x', 'EQUAL', 2)), F('x', 'IN', [1, 2])] } } }), 'rule x in [1,2]; and(or(x == 1, x == 2), x in [1,2])');
  const q11 = col('q11'), q11d = { a: { x: 1 }, b: { y: 1 }, c: { x: 2, y: 2 } };
  Q('q11.or-x-y', q11, q11d, q(q11, { where: OR(F('x', 'EQUAL', 1), F('y', 'EQUAL', 1)) }), 'rule x == 1 || y == 1; or(x == 1, y == 1)');
  Q('q11.x', q11, q11d, q(q11, { where: F('x', 'EQUAL', 1) }), 'rule x == 1 || y == 1; where x == 1');
  Q('q11.y', q11, q11d, q(q11, { where: F('y', 'EQUAL', 1) }), 'rule x == 1 || y == 1; where y == 1');
  Q('q11.and-x-y', q11, q11d, q(q11, { where: { compositeFilter: { op: 'AND', filters: [F('x', 'EQUAL', 1), F('y', 'EQUAL', 1)] } } }), 'rule x == 1 || y == 1; where x == 1 and y == 1');
  Q('q11.x-2', q11, q11d, q(q11, { where: F('x', 'EQUAL', 2) }), 'rule x == 1 || y == 1; where x == 2');
  const cg = col('cg'), cg2 = col('cg2'), top = col('top');
  const cgd = { [doc(cg, 'a')]: { pub: true }, [doc(cg, 'b')]: { pub: false }, [`${top}/p/${cg}/c`]: { pub: true }, [`${top}/p/${cg}/d`]: { pub: false } };
  const cg2d = { [doc(cg2, 'a')]: { pub: true }, [`${top}/p/${cg2}/b`]: { pub: true } };
  const CG = (id, sq, desc, seed = cgd, parent = ROOT) => add('queries', id, desc, { seed, ops: [{ op: 'runQuery', parent, structuredQuery: sq }] });
  CG('cg.group-pub', q(cg, { group: true, where: F('pub', 'EQUAL', true) }), 'rule at match /{path=**}/cg/{d}: collection group query where pub == true');
  CG('cg.group-no-where', q(cg, { group: true }), 'collection group query with no filter');
  CG('cg.group-pub-false', q(cg, { group: true, where: F('pub', 'EQUAL', false) }), 'collection group query where pub == false');
  CG('cg.collection-pub', q(cg, { where: F('pub', 'EQUAL', true) }), 'plain collection query on the same id under the {path=**} match');
  CG('cg.subcollection-pub', q(cg, { where: F('pub', 'EQUAL', true) }), 'query on the subcollection of a document: parent top/p', cgd, nm(`${top}/p`));
  CG('cg.subcollection-group-pub', q(cg, { group: true, where: F('pub', 'EQUAL', true) }), 'collection group query scoped to the descendants of top/p', cgd, nm(`${top}/p`));
  CG('cg2.group-pub', q(cg2, { group: true, where: F('pub', 'EQUAL', true) }), 'collection group query where the rule is a plain match /cg2/{d} (no {path=**})', cg2d);
  CG('cg2.collection-pub', q(cg2, { where: F('pub', 'EQUAL', true) }), 'plain collection query under the same plain match', cg2d);

  // -- 7. batches and transactions --
  const ok = (n) => doc(m1, `ok-${n}`), bad = (n) => doc(m1, `bad-${n}`);
  const bt = [ok('b1'), ok('b2'), bad('b1'), bad('b2')];
  add('batches', 'commit.ok-ok', 'commit of two allowed writes', { touch: bt, ops: [{ op: 'commit', writes: [W.set(ok('b1'), { a: 1 }), W.set(ok('b2'), { a: 1 })] }] });
  add('batches', 'commit.ok-bad', 'commit of an allowed write then a denied one: nothing is written', { touch: bt, ops: [{ op: 'commit', writes: [W.set(ok('b1'), { a: 1 }), W.set(bad('b1'), { a: 1 })] }] });
  add('batches', 'commit.bad-ok', 'commit of a denied write then an allowed one', { touch: bt, ops: [{ op: 'commit', writes: [W.set(bad('b1'), { a: 1 }), W.set(ok('b1'), { a: 1 })] }] });
  add('batches', 'commit.ok-ok-bad', 'commit of two allowed writes then a denied one', { touch: bt, ops: [{ op: 'commit', writes: [W.set(ok('b1'), { a: 1 }), W.set(ok('b2'), { a: 1 }), W.set(bad('b1'), { a: 1 })] }] });
  add('batches', 'commit.bad-bad', 'commit of two denied writes', { touch: bt, ops: [{ op: 'commit', writes: [W.set(bad('b1'), { a: 1 }), W.set(bad('b2'), { a: 1 })] }] });
  add('batches', 'commit.ok-delete-bad', 'commit of an allowed set and a denied delete', { seed: { [bad('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'commit', writes: [W.set(ok('b1'), { a: 1 }), W.del(bad('b1'))] }] });
  add('batches', 'commit.ok-bad-precondition', 'commit of an allowed write and a denied write that also has a failing precondition (exists:true on a missing doc)', { touch: bt, ops: [{ op: 'commit', writes: [W.set(ok('b1'), { a: 1 }), W.set(bad('b1'), { a: 1 }, { exists: true, mask: ['a'] })] }] });
  add('batches', 'commit.precondition-before-denied', 'commit of a failing precondition on an allowed document, then a denied write', { touch: bt, ops: [{ op: 'commit', writes: [W.set(ok('b1'), { a: 1 }, { exists: true, mask: ['a'] }), W.set(bad('b1'), { a: 1 })] }] });
  add('batches', 'batch-write.ok-bad-ok', 'batchWrite (not atomic) of allowed, denied, allowed', { touch: bt, ops: [{ op: 'batchWrite', writes: [W.set(ok('b1'), { a: 1 }), W.set(bad('b1'), { a: 1 }), W.set(ok('b2'), { a: 1 })] }] });
  add('batches', 'batch-write.bad-bad', 'batchWrite of two denied writes', { touch: bt, ops: [{ op: 'batchWrite', writes: [W.set(bad('b1'), { a: 1 }), W.set(bad('b2'), { a: 1 })] }] });
  add('batches', 'batch-write.ok-ok', 'batchWrite of two allowed writes', { touch: bt, ops: [{ op: 'batchWrite', writes: [W.set(ok('b1'), { a: 1 }), W.set(ok('b2'), { a: 1 })] }] });
  add('batches', 'tx.read-allowed', 'transaction: begin, read an allowed document, rollback', { seed: { [ok('b1')]: { a: 1 } }, ops: [{ op: 'begin' }, { op: 'batchGet', transaction: '$t', documents: [nm(ok('b1'))] }, { op: 'rollback', transaction: '$t' }] });
  add('batches', 'tx.read-denied', 'transaction: begin, read a denied document, rollback', { seed: { [bad('b1')]: { a: 1 } }, ops: [{ op: 'begin' }, { op: 'batchGet', transaction: '$t', documents: [nm(bad('b1'))] }, { op: 'rollback', transaction: '$t' }] });
  add('batches', 'tx.get-denied', 'transaction: getDocument of a denied document inside it', { seed: { [bad('b1')]: { a: 1 } }, ops: [{ op: 'begin' }, { op: 'get', transaction: '$t', name: nm(bad('b1')) }, { op: 'rollback', transaction: '$t' }] });
  add('batches', 'tx.query-denied', 'transaction: runQuery denied by the rules inside it', { seed: qd(q1, q1d), ops: [{ op: 'begin' }, { op: 'runQuery', transaction: '$t', parent: ROOT, structuredQuery: q(q1) }, { op: 'rollback', transaction: '$t' }] });
  add('batches', 'tx.query-allowed', 'transaction: runQuery allowed by the rules inside it', { seed: qd(q1, q1d), ops: [{ op: 'begin' }, { op: 'runQuery', transaction: '$t', parent: ROOT, structuredQuery: q(q1, { where: F('x', 'EQUAL', 1) }) }, { op: 'rollback', transaction: '$t' }] });
  add('batches', 'tx.commit-allowed', 'transaction: read an allowed document and commit an allowed write', { seed: { [ok('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'begin' }, { op: 'batchGet', transaction: '$t', documents: [nm(ok('b1'))] }, { op: 'commit', transaction: '$t', writes: [W.set(ok('b2'), { a: 2 })] }] });
  add('batches', 'tx.commit-denied', 'transaction: commit of a denied write', { touch: bt, ops: [{ op: 'begin' }, { op: 'commit', transaction: '$t', writes: [W.set(bad('b1'), { a: 1 })] }] });
  add('batches', 'tx.commit-ok-bad', 'transaction: commit of an allowed and a denied write', { touch: bt, ops: [{ op: 'begin' }, { op: 'commit', transaction: '$t', writes: [W.set(ok('b1'), { a: 1 }), W.set(bad('b1'), { a: 1 })] }] });
  add('batches', 'tx.read-denied-then-commit', 'transaction: a denied read, then a commit of an allowed write on the same transaction', { seed: { [bad('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'begin' }, { op: 'batchGet', transaction: '$t', documents: [nm(bad('b1'))] }, { op: 'commit', transaction: '$t', writes: [W.set(ok('b1'), { a: 1 })] }] });
  add('batches', 'tx.read-only-denied', 'read-only transaction: a denied read', { seed: { [bad('b1')]: { a: 1 } }, ops: [{ op: 'begin', options: { readOnly: {} } }, { op: 'batchGet', transaction: '$t', documents: [nm(bad('b1'))] }] });
  add('batches', 'tx.new-transaction-denied', 'batchGet with newTransaction on a denied document', { seed: { [bad('b1')]: { a: 1 } }, ops: [{ op: 'batchGet', documents: [nm(bad('b1'))], newTransaction: { readWrite: {} } }] });
  add('batches', 'tx.rollback-after-denied-commit', 'transaction: a denied commit, then rollback of the same transaction', { touch: bt, ops: [{ op: 'begin' }, { op: 'commit', transaction: '$t', writes: [W.set(bad('b1'), { a: 1 })] }, { op: 'rollback', transaction: '$t' }] });
  return cases;
}

function set2(c, id, data) {
  return [{ op: 'commit', writes: [W.set(doc(c, id), data)] }];
}

// ---- Listen and Write streams ----

async function frames(method, requests, { settleMs = 4000, between = [] } = {}) {
  const t0 = Date.now();
  const events = [];
  const call = await grpcDuplex(method);
  let closed = false;
  call.on('data', (d) => events.push({ t: Date.now() - t0, dir: 'recv', msg: d }));
  call.on('error', (e) => {
    closed = true;
    events.push({ t: Date.now() - t0, dir: 'status', status: { code: e.code, details: e.details } });
  });
  call.on('end', () => {
    closed = true;
    events.push({ t: Date.now() - t0, dir: 'end' });
  });
  for (const step of requests) {
    if (typeof step === 'function') {
      events.push({ t: Date.now() - t0, dir: 'action', note: step.label || 'action' });
      await step();
      await sleep(settleMs);
      continue;
    }
    if (closed) {
      events.push({ t: Date.now() - t0, dir: 'note', note: 'stream already closed before this request' });
      break;
    }
    assertMessageScoped(step);
    events.push({ t: Date.now() - t0, dir: 'send', msg: step });
    call.write(step);
    await sleep(settleMs);
  }
  const open = !closed;
  if (open) {
    events.push({ t: Date.now() - t0, dir: 'note', note: 'stream still open after the last wait; cancelled by the recorder' });
    call.cancel();
    await sleep(200);
  }
  return { events, open };
}

function frameCase(id, description, actor, setup, method, r) {
  const events = r.events.map((e, i) => {
    const f = { seq: i, tMs: Math.round(e.t / 100) * 100, dir: e.dir };
    if (e.note) f.note = e.note;
    if (e.status) f.status = e.status;
    if (e.msg) {
      f.msg = JSON.parse(relProject(JSON.stringify(normalise(e.msg), (k, x) => (k === 'streamId' ? '<stream_id>' : x))));
      if (e.dir === 'recv') {
        const m = e.msg;
        f.kind = m.targetChange ? `target_change:${m.targetChange.targetChangeType || 'NO_CHANGE'}${m.targetChange.targetIds?.length ? '' : ':global'}` : Object.keys(m)[0];
      }
    }
    return f;
  });
  return { id, description, actor, transport: 'grpc', rpc: `google.firestore.v1.Firestore/${method}`, setup: JSON.parse(relProject(JSON.stringify(setup))), streamOpenAtEnd: r.open, frames: events };
}

const addT = (id, ids, extra = {}) => gv({ targetId: id, documents: { documents: ids.map(nm) }, ...extra });
const qT = (id, sq, parent = ROOT) => gv({ targetId: id, query: { parent, structuredQuery: sq } });
const addReq = (target) => ({ database: DB, addTarget: target });

async function listenCases() {
  const LS = col('ls'), LS2 = col('ls2'), LS2F = col('ls2f'), A = col('a');
  const out = [];
  const seedLS = { [doc(LS, 'ok-1')]: { pub: true }, [doc(LS, 'bad-1')]: { pub: true }, [doc(LS, 'ok-2')]: { pub: false } };
  const go = async (id, desc, actor, seed, steps, o = {}) => {
    log(`${id} `);
    await admin([...(o.clear || []).map((p) => W.del(p)), ...Object.entries(seed).map(([p, d]) => W.set(p, d))]);
    await as(actor);
    const r = await frames('listen', typeof steps === 'function' ? steps() : steps, o);
    out.push(frameCase(id, desc, actor, seed, 'Listen', r));
    log(`${r.open ? 'open' : 'closed'}\n`);
    await as('owner');
  };
  const adminDo = (label, writes) => {
    const f = async () => {
      await admin(writes);
    };
    f.label = label;
    return f;
  };
  await go('listen.doc-denied', 'document targets the user cannot read (an existing document, a missing one), then a readable one on the same stream', 'user', seedLS, [
    addReq(addT(1, [doc(LS, 'bad-1')])),
    addReq(addT(2, [doc(LS, 'bad-missing')])),
    addReq(addT(3, [doc(LS, 'ok-1')])),
    addReq(addT(4, [doc(LS, 'ok-missing')])),
  ], { clear: [doc(LS, 'bad-missing'), doc(LS, 'ok-missing')] });
  await go('listen.doc-denied-then-update', 'a readable document target followed by a write of the document while a denied target sits on the same stream', 'user', seedLS, [
    addReq(addT(1, [doc(LS, 'bad-1')])),
    addReq(addT(2, [doc(LS, 'ok-1')])),
    adminDo('admin updates ok-1', [W.set(doc(LS, 'ok-1'), { pub: true, n: 2 })]),
  ]);
  await go('listen.query-allowed', 'a query target the rule allows (where pub == true), then a document leaves it and another joins', 'user', seedLS, [
    addReq(qT(1, q(LS, { where: F('pub', 'EQUAL', true) }))),
    adminDo('admin sets ok-1 pub=false and ok-2 pub=true', [W.set(doc(LS, 'ok-1'), { pub: false }), W.set(doc(LS, 'ok-2'), { pub: true })]),
  ]);
  await go('listen.query-denied', 'a query target the rule denies (no filter), then a query target the rule allows on the same stream', 'user', seedLS, [
    addReq(qT(1, q(LS))),
    addReq(qT(2, q(LS, { where: F('pub', 'EQUAL', true) }))),
  ]);
  await go('listen.query-denied-other-filter', 'a query target with a filter the rule does not imply (pub == false)', 'user', seedLS, [addReq(qT(1, q(LS, { where: F('pub', 'EQUAL', false) })))]);
  await go('listen.remove-after-denied', 'remove_target of a target that was refused, then a new target with the same id', 'user', seedLS, [
    addReq(addT(1, [doc(LS, 'bad-1')])),
    { database: DB, removeTarget: 1 },
    addReq(addT(1, [doc(LS, 'ok-1')])),
  ]);
  await go('listen.query-becomes-denied', 'a query target allowed by a lookup rule (list needs ls2f/flag.open == true); the flag is then set false and a document written', 'user', {
    [doc(LS2F, 'flag')]: { open: true },
    [doc(LS2, 'a')]: { x: 1 },
  }, [
    addReq(qT(1, q(LS2))),
    adminDo('admin sets flag.open=false', [W.set(doc(LS2F, 'flag'), { open: false })]),
    adminDo('admin writes ls2/b', [W.set(doc(LS2, 'b'), { x: 2 })]),
    adminDo('admin sets flag.open=true again', [W.set(doc(LS2F, 'flag'), { open: true })]),
    adminDo('admin writes ls2/c', [W.set(doc(LS2, 'c'), { x: 3 })]),
  ], { settleMs: 5000 });
  await go('listen.query-denied-by-lookup', 'the same query target opened while the flag is already false', 'user', {
    [doc(LS2F, 'flag')]: { open: false },
    [doc(LS2, 'a')]: { x: 1 },
  }, [addReq(qT(1, q(LS2)))]);
  await go('listen.doc-becomes-unreadable', 'a document target on ok-1 (readable by its id); the document is deleted and rewritten', 'user', seedLS, [
    addReq(addT(1, [doc(LS, 'ok-1')])),
    adminDo('admin deletes ok-1', [W.del(doc(LS, 'ok-1'))]),
    adminDo('admin rewrites ok-1', [W.set(doc(LS, 'ok-1'), { pub: true })]),
  ]);
  await go('listen.unauthenticated-allowed', 'unauthenticated document target whose get rule allows request.auth == null', 'none', { [doc(A, 'authnull')]: { a: 1 }, [doc(A, 'uid-a')]: { a: 1 } }, [
    addReq(addT(1, [doc(A, 'authnull')])),
    addReq(addT(2, [doc(A, 'uid-a')])),
  ]);
  await go('listen.owner-same-targets', 'the same document targets as listen.doc-denied, with the owner token (rules bypassed)', 'owner', seedLS, [addReq(addT(1, [doc(LS, 'bad-1')])), addReq(qT(2, q(LS)))]);
  // Write stream
  const M1 = col('m1');
  log('write.denied ');
  await admin([W.del(doc(M1, 'ok-w1')), W.del(doc(M1, 'bad-w1'))]);
  await as('user');
    const wr = await frames('write', [{ database: DB }], { settleMs: 1500 });
  out.push(frameCase('write.handshake', 'Write stream handshake as an end user', 'user', {}, 'Write', wr));
  log('\n');
  for (const [id, desc, writes] of [
    ['write.denied', 'Write stream: a batch with one denied write', [W.set(doc(M1, 'bad-w1'), { a: 1 })]],
    ['write.allowed', 'Write stream: a batch with one allowed write', [W.set(doc(M1, 'ok-w1'), { a: 1 })]],
    ['write.ok-bad', 'Write stream: a batch with an allowed and a denied write', [W.set(doc(M1, 'ok-w1'), { a: 1 }), W.set(doc(M1, 'bad-w1'), { a: 1 })]],
  ]) {
    log(`${id} `);
    await admin([W.del(doc(M1, 'ok-w1')), W.del(doc(M1, 'bad-w1'))]);
    await as('user');
    // Open a fresh stream, do the handshake, then send the batch with the stream id and token.
    const t0 = Date.now();
    const events = [];
    const call = await grpcDuplex('write');
    let closed = false;
    let hs;
    call.on('data', (d) => {
      events.push({ t: Date.now() - t0, dir: 'recv', msg: d });
      if (d.streamId && !hs) hs = d;
    });
    call.on('error', (e) => {
      closed = true;
      events.push({ t: Date.now() - t0, dir: 'status', status: { code: e.code, details: e.details } });
    });
    call.on('end', () => {
      closed = true;
      events.push({ t: Date.now() - t0, dir: 'end' });
    });
    const h = { database: DB };
    events.push({ t: 0, dir: 'send', msg: h });
    call.write(h);
    for (let i = 0; i < 40 && !hs; i++) await sleep(100);
    if (hs) {
      const b = { database: DB, streamId: hs.streamId, streamToken: hs.streamToken, writes: gv(writes) };
      assertMessageScoped(b);
      events.push({ t: Date.now() - t0, dir: 'send', msg: b });
      call.write(b);
      await sleep(3000);
    }
    const open = !closed;
    if (open) {
      events.push({ t: Date.now() - t0, dir: 'note', note: 'stream still open after the batch; cancelled by the recorder' });
      call.cancel();
      await sleep(200);
    }
    const rr = { events, open };
    out.push(frameCase(id, desc, 'user', {}, 'Write', rr));
    const back = await readBack([doc(M1, 'ok-w1'), doc(M1, 'bad-w1')]);
    out[out.length - 1].after = back;
    log(`${open ? 'open' : 'closed'}\n`);
    await as('owner');
  }
  return out;
}

// ---- main ----

async function main() {
  if (!grpcAvailable()) throw new Error('FIRESTORE_PROTOS is not set: the recording needs gRPC as well as REST');
  const startedAt = new Date().toISOString();
  let ruleset;
  let cleanupDone = false;
  const wantTopics = ['methods', 'auth', 'request', 'lookups', 'queries', 'batches'].filter(wanted);
  try {
    log('creating users\n');
    await makeUsers(randomBytes(3).toString('hex'));
    const claims = { password: claimNames(tokenOf.A), anonymous: claimNames(tokenOf.anon) };
    const source = buildRules({ uidA: state.users.A.uid, uidB: state.users.B.uid, emailA: state.users.A.email });
    log('deploying rules\n');
    ruleset = await deployRules(source);
    // fixtures
    const fx = [];
    for (let i = 1; i <= 21; i++) fx.push(W.set(`${col('fx')}/${i}`, i === 2 ? { v: 1, s: 'two' } : { v: 1 }));
    await admin(fx);
    const authDocs = ['authnull', 'authnotnull', 'uid-a', 'uid-b', 'uid-is-string', 'email', 'verified-false', 'verified-true', 'verified-bool', 'prov-password', 'prov-anonymous', 'ident-email', 'sub-is-uid', 'aud', 'iss', 'auth-time-int', 'iat-int', 'exp-int', 'firebase-map', 'token-map', 'auth-keys', 'auth-keys-only', ...['name', 'email', 'email_verified', 'sub', 'iss', 'aud', 'iat', 'exp', 'auth_time', 'user_id', 'firebase', 'picture', 'phone_number', 'admin', 'uid', 'nonce', 'azp'].map((k) => `tok_${k}`), ...['sign_in_provider', 'identities', 'tenant', 'sign_in_second_factor', 'second_factor_identifier'].map((k) => `fb_${k}`)];
    await admin(authDocs.map((d) => W.set(`${col('a')}/${d}`, { a: 1 })));
    // wait for the rules to propagate: a user get that only the new rules allow
    // Rules take minutes to reach every replica (a first recording saw answers from the previous
    // ruleset for about five minutes, on one transport and then the other), so wait, then require
    // a run of OK canaries on both transports.
    log('waiting for the rules to propagate\n');
    await sleep(240_000);
    let streak = 0;
    for (let i = 0; i < 200 && streak < 8; i++) {
      await as('user');
      const r1 = await rest('GET', `${DB}/documents/${col('a')}/authnotnull`);
      const r2 = await grpcUnary('getDocument', { name: `${ROOT}/${col('a')}/authnotnull` });
      streak = r1.http === 200 && r2.ok ? streak + 1 : 0;
      await sleep(3000);
    }
    if (streak < 8) throw new Error('the deployed rules did not take effect on both transports');
    log('rules live\n');
    const cases = buildCases(state.users);
    // the clock case needs the client's current time
    for (const c of cases.request) {
      if (c.clockField) c.ops = [{ op: 'commit', writes: [W.set(doc(col('t'), 'c-clock'), { c: 0 })] }];
    }
    const LIMIT = Number(process.env.F5_LIMIT || 0);
    for (const topic of wantTopics) for (const c of LIMIT ? cases[topic].slice(0, LIMIT) : cases[topic]) {
      if (c.id === 't.c-clock') c.ops = [{ op: 'commit', writes: [{ update: { name: nm(doc(col('t'), 'c-clock')), fields: { c: { timestampValue: new Date().toISOString() } } } }] }];
      await runCase(topic, c);
    }
    if (wanted('listen')) files.listen = await listenCases();
    // write the corpus
    const recordedAt = `${startedAt} .. ${new Date().toISOString()}`;
    const header = (notes) => ({
      oracle: 'production',
      target: `${PROJECT} (default): FIRESTORE_NATIVE, STANDARD, PESSIMISTIC, europe-west1; security rules deployed by the recorder (f5-rules-deployed.json)`,
      generator: GENERATOR,
      recordedAt,
      versions: versions({ api: 'firestore.googleapis.com REST v1 and gRPC google.firestore.v1.Firestore; firebaserules.googleapis.com v1; identitytoolkit.googleapis.com v1', ...grpcVersions() }),
      notes: [
        'Auth: an end-user Firebase Auth ID token (the caller field names the actor: user and userB are email/password users, anon is an anonymous user, none sends the web API key only, owner is the project owner whose token bypasses rules). The owner only seeds and reads back state.',
        'Rules: f5-rules-deployed.json holds the source every case ran under. Each case names the document ids its rule asserts; "allow" and "deny" are OK and PERMISSION_DENIED.',
        'Times, durations and transaction ids are replaced by placeholders; everything else is as returned. A case ran once over REST and once over gRPC, each from the seeded state in "seed" (documents in "touch" are reset first); "after" is the state read back by the owner afterwards.',
        ...notes,
      ],
    });
    const topicNotes = {
      methods: [], auth: [`The users' own uids appear only inside the rules; ${A_NOTE}`], request: [], lookups: [], queries: [], batches: [], listen: ['Listen and Write are gRPC only. Frames carry their time in ms from the stream opening (rounded to 100 ms).'],
    };
    if (!only.length) writeCorpus('f5-rules-deployed.json', header([]), {
      rules: { file: 'firestore.rules', source, rulesetCreatedFor: 'this recording; released as cloud.firestore, then the original ruleset was restored' },
      userClaims: claims,
      users: { A: { provider: 'password', emailVerified: false }, B: { provider: 'password', emailVerified: false }, anon: { provider: 'anonymous' } },
      cases: [],
    });
    for (const topic of [...wantTopics, ...(files.listen ? ['listen'] : [])]) {
      writeCorpus(`f5-rules-${topic}.json`, header(topicNotes[topic]), { rules: 'corpus/f5-rules-deployed.json', cases: files[topic] });
    }
  } finally {
    // cleanup, restoring the rules, and deleting users
    try {
      await as('owner');
      log('\ncleanup: documents\n');
      const del = [...written].map((n) => ({ delete: n }));
      for (let i = 0; i < del.length; i += 200) {
        const r = await rest('POST', `${DB}/documents:commit`, { writes: del.slice(i, i + 200) });
        if (r.http !== 200) log(`cleanup commit HTTP ${r.http}\n`);
      }
      // sweep what the recorder tracks: every collection id it uses, any depth
      const left = [];
      for (const cid of new Set([...written].map((n) => rel(n).split('/')[0]))) {
        const r = await rest('GET', `${DB}/documents/${cid}?showMissing=true&pageSize=300`);
        for (const d of r.body.documents || []) left.push(rel(d.name));
      }
      const sweepGroups = [col('cg'), col('cg2')];
      for (const gid of sweepGroups) {
        const r = await rest('POST', `${DB}/documents:runQuery`, { structuredQuery: { from: [{ collectionId: gid, allDescendants: true }] } });
        for (const x of r.body) if (x.document) left.push(rel(x.document.name));
      }
      globalThis.leftover = left;
      log(`left after cleanup: ${left.length}\n`);
    } catch (e) {
      log(`cleanup failed: ${e.message}\n`);
    }
    try {
      const rel_ = await rulesCall('PATCH', `projects/${PROJECT}/releases/cloud.firestore`, { release: { name: `projects/${PROJECT}/releases/cloud.firestore`, rulesetName: ORIGINAL_RULESET } });
      log(`restore release: HTTP ${rel_.http}\n`);
      if (ruleset) await rulesCall('DELETE', ruleset);
    } catch (e) {
      log(`restore failed: ${e.message}\n`);
    }
    try {
      for (const uid of state.uids) await toolkit('accounts:delete', { localId: uid, targetProjectId: PROJECT }, true);
      log(`deleted ${state.uids.length} users\n`);
    } catch (e) {
      log(`user deletion failed: ${e.message}\n`);
    }
    cleanupDone = true;
  }
  if (!only.length || only.includes('cleanup')) {
    writeCorpus('f5-rules-cleanup.json', { oracle: 'production', target: PROJECT, generator: GENERATOR, recordedAt: new Date().toISOString(), versions: versions(), notes: ['Everything the recorder wrote under aruma-ref-f5-* was deleted and the collections swept; the original rules were restored and the test users deleted.'] }, {
      cases: [{ id: 'cleanup.sweep', description: 'documents left under the aruma-ref-f5 collections after the deletion, and the owner-side calls made', documentsLeft: globalThis.leftover || [], documentsWritten: written.size, ownerCalls: calls }],
    });
  }
}
const A_NOTE = 'tokens are never stored.';

main().catch((e) => {
  log(`\nFAILED: ${e.message}\n`);
  process.exit(1);
});
