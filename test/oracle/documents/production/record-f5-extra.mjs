// F5 production recordings, second recorder, written after record-f5-rules.mjs
// (it shares that recorder's plumbing, as a copy, and deploys the same rules plus a few
// matches). It records two things the first one did not get right or was not asked for:
//
//   f5-rules-transactions.json  transactions under rules: BeginTransaction as an end user
//                               is refused, so the transactions here open with
//                               new_transaction on the first read, as the client SDKs do

//   f5-rules-constraints.json   rule x != null / x in [...] / n < 10 / n > 0 /
//                               request.query.limit <= 10 / x == 'a' / n == 3 /
//                               z > -1 against queries whose filters imply, or
//                               nearly imply, the condition
//
//   node record-f5-extra.mjs
//
// Same environment, identities, tidiness and restore of the original rules as
// record-f5-rules.mjs (read its header).
import { randomBytes } from 'node:crypto';
import { readFileSync } from 'node:fs';
import {
  PROJECT, DB, ROOT, PREFIX, rest, v, fields, versions, writeCorpus, assertScoped, assertMessageScoped, normalise, compact,
  grpcAvailable, grpcUnary, grpcStream, grpcDuplex, grpcVersions, identity, accessToken,
} from './lib.mjs';

const GENERATOR = 'test/oracle/documents/production/record-f5-extra.mjs';
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

function buildBaseRules({ uidA, uidB, emailA }) {
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
    match /{path=**}/${col('kg')}/{d} {
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

// ---- rules ----

const HW = (d) => `/databases/$(database)/documents/${col('hw')}/${d}`;

function buildExtraMatches() {
  const c = (n, cond) => `    match /${col(n)}/{d} {\n      allow list: if ${cond};\n    }\n`;
  return `${c('ka', 'resource.data.x != null')}${c('kb', "resource.data.x in ['a', 'b']")}${c('kc', 'resource.data.n < 10')}${c('kd', 'resource.data.n > 0')}${c('ke', 'request.query.limit <= 10')}${c('kf', "resource.data.x == 'a'")}${c('kg', 'resource.data.n == 3')}${c('kh', 'resource.data.z > -1')}    match /${col('cx')}/{d} {
      allow get: if true;
    }
    match /${col('hl')}/{d} {
      allow get: if ${or([
        `d.matches('r1-.*') && ${gets(10, 'get', 1)}`, `d.matches('r2-.*') && ${gets(10, 'get', 11)}`, `d.matches('r3-.*') && ${gets(10, 'get', 21)}`, `d.matches('r4-.*') && ${gets(10, 'get', 31)}`,
        `d.matches('r11-.*') && ${gets(11)}`, `d.matches('r20-.*') && ${gets(20)}`, `d.matches('r21-.*') && ${gets(21)}`,
        `d == 'miss-exists' && ${gets(10, 'get', 1)} && (exists(${FX('none1')}) || true)`,
        `d == 'miss-after' && ${gets(10, 'get', 1)} && (existsAfter(${FX('none1')}) || true)`,
        `d == 'live-after' && ${gets(10, 'get', 1)} && (existsAfter(${FX(11)}) || true)`,
      ])};
    }
    match /${col('hw')}/{d} {
      allow write: if ${or([
        `d.matches('r1-.*') && ${gets(10, 'get', 1)}`, `d.matches('r2-.*') && ${gets(10, 'get', 11)}`, `d.matches('r3-.*') && ${gets(10, 'get', 21)}`, `d.matches('r4-.*') && ${gets(10, 'get', 31)}`,
        `d.matches('r11-.*') && ${gets(11)}`, `d.matches('r20-.*') && ${gets(20)}`, `d.matches('r21-.*') && ${gets(21)}`,
        `d == 'miss-exists' && ${gets(10, 'get', 1)} && (exists(${FX('none1')}) || true)`,
        `d == 'miss-after' && ${gets(10, 'get', 1)} && (existsAfter(${FX('none1')}) || true)`,
        `d == 'live-after' && ${gets(10, 'get', 1)} && (existsAfter(${FX(11)}) || true)`,
        `d == 'peer-a' && ${gets(10, 'get', 1)}`,
        `d == 'peer-b' && ${gets(10, 'get', 11)} && existsAfter(${HW('peer-a')})`,
        `d == 'peer-c' && ${gets(10, 'get', 11)} && (existsAfter(${HW('peer-a')}) || true)`,
        `d == 'peer-b2' && ${gets(10, 'get', 11)} && (exists(${HW('peer-a')}) || true)`,
        `d == 'peer-b3' && ${gets(10, 'get', 11)} && (get(${HW('peer-a')}).data.a == 1 || true)`,
        `d == 'self-after' && ${gets(10, 'get', 1)} && existsAfter(${HW('self-after')})`,
        `d == 'self-exists' && ${gets(10, 'get', 1)} && (exists(${HW('self-exists')}) || true)`,
        `d == 'self-get' && ${gets(10, 'get', 1)} && (get(${HW('self-get')}).data.a == 0 || true)`,
      ])};
    }
    match /${col('hq1')}/{d} {
      allow list: if ${gets(10)};
    }
    match /${col('hq2')}/{d} {
      allow list: if ${gets(11)};
    }
    match /${col('hq3')}/{d} {
      allow list: if ${gets(20)};
    }
    match /${col('hq4')}/{d} {
      allow list: if ${gets(21)};
    }
    match /{path=**}/${col('cgt')}/{d} {
      allow read: if true;
    }
    match /${col('cgu')}/{d} {
      allow read: if true;
    }
    match /{path=**}/${col('cgv')}/{d} {
      allow read: if request.auth != null;
    }
`;
}


function buildRules(users) {
  const base = buildBaseRules(users);
  const i = base.lastIndexOf('  }\n}');
  return base.slice(0, i) + buildExtraMatches() + base.slice(i);
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
      if (k === 'doubleValue' && typeof y === 'string') return [k, Number(y)];
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
      greq = T_({ name: op.name, ...(op.newTransaction ? { newTransaction: op.newTransaction } : {}) });
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
      rbody = T_({ database: DB, documents: op.documents, ...(op.newTransaction ? { newTransaction: op.newTransaction } : {}) });
      rpc = 'batchGetDocuments';
      stream = true;
      greq = rbody;
      break;
    case 'runQuery':
      method = 'POST';
      path = `${op.parent}:runQuery`;
      rbody = T_({ structuredQuery: op.structuredQuery, ...(op.newTransaction ? { newTransaction: op.newTransaction } : {}) });
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
    if (op.newTransaction) ctx.newTxn = (Array.isArray(r.body) ? r.body : [r.body]).find((x) => x && x.transaction)?.transaction;
    if (error) error = { http: r.http, status: error.status, message: error.message };
  } else {
    const g = gv(greq);
    const r = stream ? await grpcStream(rpc, g) : await grpcUnary(rpc, g);
    if (r.error?.code === 8 && /quota|exhausted/i.test(r.error.message || '')) throw new Error('RESOURCE_EXHAUSTED from Firestore: stopping, as agreed');
    resp = r.ok ? { ok: true, [stream ? 'responses' : 'message']: stream ? r.responses : r.response } : { ok: false, ...(stream && r.responses.length ? { responses: r.responses } : {}) };
    if (!r.ok) error = { code: r.error.code, status: r.error.status, message: r.error.message };
    if (op.op === 'begin' && r.ok) ctx.newTxn = r.response.transaction;
    if (op.newTransaction) ctx.newTxn = (stream ? r.responses || [] : [r.response]).find((x) => x && x.transaction && x.transaction.length)?.transaction;
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
      if (op.op === 'begin' || op.newTransaction) ctx.txn = ctx.newTxn;
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

function buildCases() {
  const cases = { constraints: [], transactions: [], limits: [] };
  const val = (x) => (typeof x === 'string' && x.startsWith('d:') ? { doubleValue: x.slice(2) } : v(x));
  const Fv = (f, op, x) => ({ fieldFilter: { field: { fieldPath: f }, op, value: val(x) } });
  const AND = (...fs) => ({ compositeFilter: { op: 'AND', filters: fs } });
  const add = (id, c, docs, o, desc) => cases.constraints.push({
    id, description: desc,
    seed: Object.fromEntries(Object.entries(docs).map(([k, d]) => [`${col(c)}/${k}`, d])),
    ops: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(col(c), o) }],
  });
  const xs = { a: { x: 'a' }, b: { x: 'b' } };
  const ns = { a: { n: 3 }, b: { n: 7 } };
  add('a.eq-a', 'ka', xs, { where: Fv('x', 'EQUAL', 'a') }, 'rule resource.data.x != null; where x == "a"');
  add('a.no-filter', 'ka', xs, {}, 'rule resource.data.x != null; no filter');
  add('b.eq-a', 'kb', xs, { where: Fv('x', 'EQUAL', 'a') }, "rule resource.data.x in ['a','b']; where x == 'a'");
  add('b.in-ab', 'kb', xs, { where: Fv('x', 'IN', ['a', 'b']) }, "rule resource.data.x in ['a','b']; where x in ['a','b']");
  add('b.in-abc', 'kb', xs, { where: Fv('x', 'IN', ['a', 'b', 'c']) }, "rule resource.data.x in ['a','b']; where x in ['a','b','c']");
  add('c.lt-5', 'kc', ns, { where: Fv('n', 'LESS_THAN', 5) }, 'rule resource.data.n < 10; where n < 5');
  add('c.lte-5', 'kc', ns, { where: Fv('n', 'LESS_THAN_OR_EQUAL', 5) }, 'rule resource.data.n < 10; where n <= 5');
  add('c.lt-10', 'kc', ns, { where: Fv('n', 'LESS_THAN', 10) }, 'rule resource.data.n < 10; where n < 10');
  add('c.lte-10', 'kc', ns, { where: Fv('n', 'LESS_THAN_OR_EQUAL', 10) }, 'rule resource.data.n < 10; where n <= 10');
  add('c.lte-9', 'kc', ns, { where: Fv('n', 'LESS_THAN_OR_EQUAL', 9) }, 'rule resource.data.n < 10; where n <= 9');
  add('c.eq-5', 'kc', ns, { where: Fv('n', 'EQUAL', 5) }, 'rule resource.data.n < 10; where n == 5');
  add('c.lt-5-double', 'kc', ns, { where: Fv('n', 'LESS_THAN', 'd:5') }, 'rule resource.data.n < 10; where n < 5.0 (double)');
  const n1 = { a: { n: 1 }, b: { n: 5 } };
  add('d.range-1-1', 'kd', n1, { where: AND(Fv('n', 'GREATER_THAN_OR_EQUAL', 1), Fv('n', 'LESS_THAN_OR_EQUAL', 1)) }, 'rule resource.data.n > 0; where n >= 1 and n <= 1');
  add('d.eq-1', 'kd', n1, { where: Fv('n', 'EQUAL', 1) }, 'rule resource.data.n > 0; where n == 1');
  add('d.gte-1', 'kd', n1, { where: Fv('n', 'GREATER_THAN_OR_EQUAL', 1) }, 'rule resource.data.n > 0; where n >= 1');
  add('d.gt-0', 'kd', n1, { where: Fv('n', 'GREATER_THAN', 0) }, 'rule resource.data.n > 0; where n > 0');
  add('d.gte-0', 'kd', n1, { where: Fv('n', 'GREATER_THAN_OR_EQUAL', 0) }, 'rule resource.data.n > 0; where n >= 0');
  add('d.range-1-5', 'kd', n1, { where: AND(Fv('n', 'GREATER_THAN_OR_EQUAL', 1), Fv('n', 'LESS_THAN_OR_EQUAL', 5)) }, 'rule resource.data.n > 0; where 1 <= n <= 5');
  add('e.no-limit', 'ke', xs, {}, 'rule request.query.limit <= 10; no limit');
  add('e.limit-5', 'ke', xs, { limit: 5 }, 'rule request.query.limit <= 10; limit 5');
  add('e.limit-10', 'ke', xs, { limit: 10 }, 'rule request.query.limit <= 10; limit 10');
  add('e.limit-20', 'ke', xs, { limit: 20 }, 'rule request.query.limit <= 10; limit 20');
  add('f.eq-a', 'kf', xs, { where: Fv('x', 'EQUAL', 'a') }, "rule resource.data.x == 'a'; where x == 'a'");
  add('f.in-a', 'kf', xs, { where: Fv('x', 'IN', ['a']) }, "rule resource.data.x == 'a'; where x in ['a']");
  add('f.in-aa', 'kf', xs, { where: Fv('x', 'IN', ['a', 'a']) }, "rule resource.data.x == 'a'; where x in ['a','a']");
  add('f.no-filter', 'kf', xs, {}, "rule resource.data.x == 'a'; no filter");
  add('f.or-a-a', 'kf', xs, { where: { compositeFilter: { op: 'OR', filters: [Fv('x', 'EQUAL', 'a'), Fv('x', 'EQUAL', 'a')] } } }, "rule resource.data.x == 'a'; or(x == 'a', x == 'a')");
  add('g.eq-3-double', 'kg', { a: { n: 3 } }, { where: Fv('n', 'EQUAL', 'd:3') }, 'rule resource.data.n == 3; where n == 3.0 (double)');
  add('g.eq-3-int', 'kg', { a: { n: 3 } }, { where: Fv('n', 'EQUAL', 3) }, 'rule resource.data.n == 3; where n == 3 (int)');
  add('h.eq-neg-zero', 'kh', { a: { z: 0 } }, { where: Fv('z', 'EQUAL', 'd:-0') }, 'rule resource.data.z > -1; where z == -0.0');
  add('h.eq-zero', 'kh', { a: { z: 0 } }, { where: Fv('z', 'EQUAL', 0) }, 'rule resource.data.z > -1; where z == 0');
  add('h.eq-minus-two', 'kh', { a: { z: 0 } }, { where: Fv('z', 'EQUAL', -2) }, 'rule resource.data.z > -1; where z == -2');
  const top = col('top');
  const cgSeed = (c) => ({ [`${c}/a`]: { pub: true }, [`${top}/p/${c}/b`]: { pub: true } });
  const grp = (id, c, o, desc, parent = ROOT) => cases.constraints.push({ id, description: desc, seed: cgSeed(col(c)), ops: [{ op: 'runQuery', parent, structuredQuery: q(col(c), o) }] });
  grp('cg.group-recursive-match', 'cgt', { group: true }, 'collection group query, no filter, under match /{path=**}/cgt/{d} allow read: if true');
  grp('cg.group-plain-match', 'cgu', { group: true }, 'collection group query, no filter, under a plain match /cgu/{d} allow read: if true');
  grp('cg.collection-plain-match', 'cgu', {}, 'plain collection query under the plain match');
  grp('cg.collection-recursive-match', 'cgt', {}, 'plain collection query under the {path=**} match');
  grp('cg.group-scoped-recursive-match', 'cgt', { group: true }, 'collection group query scoped to the descendants of top/p, under the {path=**} match', nm(`${top}/p`));
  grp('cg.group-auth-rule', 'cgv', { group: true }, 'collection group query under match /{path=**}/cgv/{d} allow read: if request.auth != null');
  cases.constraints.push({ id: 'cg.group-count', description: 'collection group count under the {path=**} match', seed: cgSeed(col('cgt')), ops: [{ op: 'aggregate', parent: ROOT, structuredQuery: q(col('cgt'), { group: true }) }] });
  // -- transactions --
  const HL = col('hl'), HW = col('hw');
  const M1 = col('m1'), L1 = col('l1'), L2 = col('l2'), L2T = col('l2t'), Q1 = col('q1');
  const ok = (n) => `${M1}/ok-${n}`, bad = (n) => `${M1}/bad-${n}`;
  const NT = { readWrite: {} };
  const bt = [ok('b1'), ok('b2'), bad('b1')];
  const tadd = (id, description, c) => cases.transactions.push({ id, description, ...c });
  tadd('begin.user', 'BeginTransaction (read-write, no options) as an end user', { ops: [{ op: 'begin' }] });
  tadd('begin.user-readonly', 'BeginTransaction read-only as an end user', { ops: [{ op: 'begin', options: { readOnly: {} } }] });
  tadd('begin.user-readwrite', 'BeginTransaction read-write explicitly as an end user', { ops: [{ op: 'begin', options: { readWrite: {} } }] });
  tadd('begin.anonymous', 'BeginTransaction as an anonymous user', { actor: 'anon', ops: [{ op: 'begin' }] });
  tadd('begin.none', 'BeginTransaction with the API key only (unauthenticated)', { actor: 'none', ops: [{ op: 'begin' }] });
  tadd('begin.owner', 'BeginTransaction as the project owner, then rollback', { actor: 'owner', ops: [{ op: 'begin' }, { op: 'rollback', transaction: '$t' }] });
  tadd('read.allowed-rollback', 'new_transaction on a batchGet of an allowed document, then rollback', { seed: { [ok('b1')]: { a: 1 } }, ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'rollback', transaction: '$t' }] });
  tadd('read.denied', 'new_transaction on a batchGet of a denied document', { seed: { [bad('b1')]: { a: 1 } }, ops: [{ op: 'batchGet', documents: [nm(bad('b1'))], newTransaction: NT }] });
  tadd('read.one-of-two-denied', 'new_transaction on a batchGet of an allowed and a denied document', { seed: { [ok('b1')]: { a: 1 }, [bad('b1')]: { a: 1 } }, ops: [{ op: 'batchGet', documents: [nm(ok('b1')), nm(bad('b1'))], newTransaction: NT }] });
  tadd('read.missing-allowed', 'new_transaction on a batchGet of a missing allowed document, then rollback', { touch: [ok('b1')], ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'rollback', transaction: '$t' }] });
  tadd('read.readonly-allowed', 'new_transaction read-only on an allowed document', { seed: { [ok('b1')]: { a: 1 } }, ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: { readOnly: {} } }] });
  tadd('read.readonly-denied', 'new_transaction read-only on a denied document', { seed: { [bad('b1')]: { a: 1 } }, ops: [{ op: 'batchGet', documents: [nm(bad('b1'))], newTransaction: { readOnly: {} } }] });
  tadd('query.allowed', 'new_transaction on a runQuery the rules allow, then rollback', { seed: { [`${Q1}/a`]: { x: 1 }, [`${Q1}/c`]: { x: 2 } }, ops: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(Q1, { where: Fv('x', 'EQUAL', 1) }), newTransaction: NT }, { op: 'rollback', transaction: '$t' }] });
  tadd('query.denied', 'new_transaction on a runQuery the rules deny', { seed: { [`${Q1}/a`]: { x: 1 }, [`${Q1}/c`]: { x: 2 } }, ops: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(Q1), newTransaction: NT }] });
  tadd('commit.read-then-allowed', 'read an allowed document in a new transaction, commit an allowed write', { seed: { [ok('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: [W.set(ok('b2'), { a: 2 })] }] });
  tadd('commit.read-then-denied', 'read an allowed document in a new transaction, commit a denied write', { seed: { [ok('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: [W.set(bad('b1'), { a: 1 })] }] });
  tadd('commit.read-then-ok-bad', 'read in a new transaction, commit an allowed and a denied write: nothing is written', { seed: { [ok('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: [W.set(ok('b2'), { a: 2 }), W.set(bad('b1'), { a: 1 })] }] });
  tadd('commit.denied-then-rollback', 'a denied commit in a transaction, then rollback of the same transaction', { seed: { [ok('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: [W.set(bad('b1'), { a: 1 })] }, { op: 'rollback', transaction: '$t' }] });
  tadd('commit.denied-then-retry', 'a denied commit in a transaction, then an allowed commit on the same transaction', { seed: { [ok('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: [W.set(bad('b1'), { a: 1 })] }, { op: 'commit', transaction: '$t', writes: [W.set(ok('b2'), { a: 2 })] }] });
  tadd('commit.no-transaction-readwrite-flag', 'commit that is not part of a transaction, after a read-only new_transaction read', { seed: { [ok('b1')]: { a: 1 } }, touch: bt, ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: { readOnly: {} } }, { op: 'commit', writes: [W.set(ok('b2'), { a: 2 })] }] });
  const t1 = { v: 1, n: 5 };
  tadd('lookup.getafter', 'new transaction reads the target, commit [rule doc, target v=2]: getAfter sees v=2', { seed: { [`${L2T}/t1`]: t1 }, touch: [`${L2}/ga-tx`, `${L2T}/t1`], ops: [{ op: 'batchGet', documents: [nm(`${L2T}/t1`)], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: [W.set(`${L2}/ga-tx`, { a: 1 }), W.set(`${L2T}/t1`, { v: 2 }, { mask: ['v'] })] }] });
  const l1seed = Object.fromEntries(['g10-a', 'g10-b', 'g10-c', 'g10-d'].map((i) => [`${L1}/${i}`, { p: 1 }]));
  const wr = (ids) => ids.map((i) => W.set(`${L2}/${i}`, { a: 1 }));
  tadd('limit.commit-g10x2', 'transaction commit of two writes with 10 lookups each (20)', { seed: { [ok('b1')]: { a: 1 } }, touch: ['r1-a', 'r2-a'].map((i) => `${HW}/${i}`), ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: wr(['r1-a', 'r2-a']) }] });
  tadd('limit.commit-g10x3', 'transaction commit of three writes with 10 lookups each (30)', { seed: { [ok('b1')]: { a: 1 } }, touch: ['r1-a', 'r2-a', 'r3-a'].map((i) => `${HW}/${i}`), ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: wr(['r1-a', 'r2-a', 'r3-a']) }] });
  tadd('limit.commit-g11', 'transaction commit of one write with 11 lookups', { seed: { [ok('b1')]: { a: 1 } }, touch: [`${HW}/g11-a`], ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: wr(['r11-a']) }] });
  tadd('limit.commit-g20', 'transaction commit of one write with 20 lookups', { seed: { [ok('b1')]: { a: 1 } }, touch: [`${HW}/g20-a`], ops: [{ op: 'batchGet', documents: [nm(ok('b1'))], newTransaction: NT }, { op: 'commit', transaction: '$t', writes: wr(['r20-a']) }] });
  tadd('limit.reads-two-batches', 'new transaction batchGet of two 10-lookup documents (20), then a second batchGet of two more in the same transaction', { seed: l1seed, ops: [{ op: 'batchGet', documents: ['r1-a', 'r2-a'].map((i) => nm(`${HL}/${i}`)), newTransaction: NT }, { op: 'batchGet', transaction: '$t', documents: ['r3-a', 'r4-a'].map((i) => nm(`${HL}/${i}`)) }, { op: 'rollback', transaction: '$t' }] });
  tadd('limit.reads-then-commit', 'new transaction batchGet of two 10-lookup documents (20), then a commit of one write with 10 lookups', { seed: l1seed, touch: [`${HW}/g10-a`], ops: [{ op: 'batchGet', documents: ['r1-a', 'r2-a'].map((i) => nm(`${HL}/${i}`)), newTransaction: NT }, { op: 'commit', transaction: '$t', writes: wr(['r1-a']) }] });
  // -- lookup limits, redone with distinct documents per range (a lookup of a document already looked up in the request is cached and not counted) --
  const lk = cases.limits = [];
  const ladd = (id, description, c) => lk.push({ id, description, ...c });
  const HLc = col('hl'), HWc = col('hw');
  const hls = (ids) => Object.fromEntries(ids.map((i) => [`${HLc}/${i}`, { p: 1 }]));
  const hwr = (ids, extra = []) => ({ touch: [...ids.map((i) => `${HWc}/${i}`), ...extra], ops: [{ op: 'commit', writes: ids.map((i) => W.set(`${HWc}/${i}`, { a: 1 })) }] });
  for (const id of ['r1-a', 'r11-a', 'r20-a', 'r21-a']) ladd(`read.get-${id}`, `get of ${HLc}/${id}: its rule makes the lookups of its range`, { seed: hls([id]), ops: [{ op: 'get', name: nm(`${HLc}/${id}`) }] });
  const bgl = (ids) => ({ seed: hls(ids), ops: [{ op: 'batchGet', documents: ids.map((i) => nm(`${HLc}/${i}`)) }] });
  ladd('read.batch-get-r1a-r1b', 'batchGet of two documents with the same 10 lookups (10 distinct)', bgl(['r1-a', 'r1-b']));
  ladd('read.batch-get-r1a-r2a', 'batchGet of two documents with 10 lookups each, disjoint (20 distinct)', bgl(['r1-a', 'r2-a']));
  ladd('read.batch-get-r1a-r2a-r3a', 'batchGet of three documents with 10 disjoint lookups each (30)', bgl(['r1-a', 'r2-a', 'r3-a']));
  ladd('read.batch-get-r1a-r2a-r3a-r4a', 'batchGet of four documents with 10 disjoint lookups each (40)', bgl(['r1-a', 'r2-a', 'r3-a', 'r4-a']));
  ladd('read.batch-get-r1a-r11a', 'batchGet of a 10-lookup and an 11-lookup document sharing the first 10 (11 distinct)', bgl(['r1-a', 'r11-a']));
  ladd('read.batch-get-r20a', 'batchGet of one document with 20 distinct lookups', bgl(['r20-a']));
  ladd('read.batch-get-r1a-r20a', 'batchGet of a 10-lookup and a 20-lookup document sharing the first 10 (20 distinct)', bgl(['r1-a', 'r20-a']));
  ladd('read.batch-get-r2a-r20a', 'batchGet of a document with lookups 11-20 and one with 1-20 (20 distinct)', bgl(['r2-a', 'r20-a']));
  ladd('read.batch-get-r1a-r21a', 'batchGet of a 10-lookup and a 21-lookup document (21 distinct)', bgl(['r1-a', 'r21-a']));
  for (const [c, n] of [['hq1', 10], ['hq2', 11], ['hq3', 20], ['hq4', 21]]) {
    ladd(`query.run-query-${n}`, `runQuery under a list rule with ${n} distinct lookups`, { seed: { [`${col(c)}/x`]: { p: 1 } }, ops: [{ op: 'runQuery', parent: ROOT, structuredQuery: q(col(c)) }] });
    ladd(`query.list-documents-${n}`, `listDocuments under a list rule with ${n} distinct lookups`, { seed: { [`${col(c)}/x`]: { p: 1 } }, ops: [{ op: 'list', parent: ROOT, collectionId: col(c) }] });
  }
  ladd('write.r1a', 'a single write with 10 distinct lookups', hwr(['r1-a']));
  ladd('write.r11a', 'a single write with 11 distinct lookups', hwr(['r11-a']));
  ladd('write.r20a', 'a single write with 20 distinct lookups', hwr(['r20-a']));
  ladd('write.r21a', 'a single write with 21 distinct lookups', hwr(['r21-a']));
  ladd('commit.r1a-r1b', 'commit of two writes with the same 10 lookups', hwr(['r1-a', 'r1-b']));
  ladd('commit.r1a-r2a', 'commit of two writes with 10 disjoint lookups each (20 distinct)', hwr(['r1-a', 'r2-a']));
  ladd('commit.r1a-r2a-r3a', 'commit of three writes with 10 disjoint lookups each (30)', hwr(['r1-a', 'r2-a', 'r3-a']));
  ladd('commit.r1a-r2a-r3a-r4a', 'commit of four writes with 10 disjoint lookups each (40)', hwr(['r1-a', 'r2-a', 'r3-a', 'r4-a']));
  ladd('commit.r1a-r11a', 'commit of a 10-lookup and an 11-lookup write sharing the first 10 (11 distinct; the second alone has 11)', hwr(['r1-a', 'r11-a']));
  ladd('commit.r1a-r20a', 'commit of a 10-lookup and a 20-lookup write (20 distinct; the second alone has 20)', hwr(['r1-a', 'r20-a']));
  ladd('commit.peer-a-peer-b', 'commit [peer-a (lookups 1-10), peer-b (lookups 11-20 plus existsAfter of peer-a, which the commit writes)]: 20 distinct, 21 if the written document counts', hwr(['peer-a', 'peer-b']));
  ladd('commit.peer-b-peer-a', 'the same in the other order', hwr(['peer-b', 'peer-a']));
  ladd('commit.peer-a-peer-b2', 'commit [peer-a, peer-b2 (lookups 11-20 plus exists of peer-a, written by the commit; missing before)]', hwr(['peer-a', 'peer-b2']));
  ladd('commit.peer-a-peer-b3', 'commit [peer-a, peer-b3 (lookups 11-20 plus get of peer-a, existing before with a=1)]', { seed: { [`${HWc}/peer-a`]: { a: 1 } }, ...hwr(['peer-a', 'peer-b3']) });
  ladd('write.peer-c-alone', 'peer-c alone (lookups 11-20 plus existsAfter of peer-a, which this commit does not write): 11 distinct lookups in a single write', hwr(['peer-c']));
  ladd('commit.peer-a-peer-c', 'commit [peer-a, peer-c]: 20 distinct lookups and one existsAfter of a written document', hwr(['peer-a', 'peer-c']));
  ladd('write.peer-b-alone', 'peer-b alone: its rule needs existsAfter of peer-a, which is missing, so the rule is false', hwr(['peer-b'], [`${HWc}/peer-a`]));
  for (const id of ['miss-exists', 'miss-after', 'live-after']) {
    ladd(`write.${id}`, `one write with 10 lookups plus one more (${id}: exists of a missing document, existsAfter of a missing document, existsAfter of an existing one), none of them the written document`, hwr([id]));
    ladd(`read.get-${id}`, `get of a document whose rule makes 10 lookups plus one more (${id})`, { seed: hls([id]), ops: [{ op: 'get', name: nm(`${HLc}/${id}`) }] });
  }
  ladd('write.self-after', 'one write with 10 lookups plus existsAfter of the document it writes', hwr(['self-after']));
  ladd('write.self-exists', 'one write with 10 lookups plus exists of the document it writes (missing)', hwr(['self-exists']));
  ladd('write.self-get', 'one write with 10 lookups plus get of the document it writes (existing, a is 0)', { seed: { [`${HWc}/self-get`]: { a: 0 } }, ...hwr(['self-get']) });
  ladd('batch-write.r1a-r2a', 'batchWrite (the BatchWrite API) of two allowed writes with 10 distinct lookups each', { touch: [`${HWc}/r1a`, `${HWc}/r2a`], ops: [{ op: 'batchWrite', writes: ['r1-a', 'r2-a'].map((i) => W.set(`${HWc}/${i}`, { a: 1 })) }] });
  return cases;
}

// ---- main ----

async function main() {
  if (!grpcAvailable()) throw new Error('FIRESTORE_PROTOS is not set');
  const startedAt = new Date().toISOString();
  let ruleset;
  try {
    log('creating users\n');
    await makeUsers(randomBytes(3).toString('hex'));
    const source = buildRules({ uidA: state.users.A.uid, uidB: state.users.B.uid, emailA: state.users.A.email });
    ruleset = await deployRules(source);
    const fx = [];
    for (let i = 1; i <= 41; i++) fx.push(W.set(`${col('fx')}/${i}`, i === 2 ? { v: 1, s: 'two' } : { v: 1 }));
    await admin(fx);
    await admin([W.set(`${col('cx')}/canary`, { a: 1 })]);
    // Rules take minutes to reach every replica (a first recording saw answers from the previous
    // ruleset for about five minutes, on one transport and then the other), so wait, then require
    // a run of OK canaries on both transports.
    log('waiting for the rules to propagate\n');
    await sleep(240_000);
    let streak = 0;
    for (let i = 0; i < 200 && streak < 8; i++) {
      await as('user');
      const r1 = await rest('GET', `${DB}/documents/${col('cx')}/canary`);
      const r2 = await grpcUnary('getDocument', { name: `${ROOT}/${col('cx')}/canary` });
      streak = r1.http === 200 && r2.ok ? streak + 1 : 0;
      await sleep(3000);
    }
    if (streak < 8) throw new Error('the deployed rules did not take effect on both transports');
    log('rules live\n');
    const cases = buildCases();
    for (const c of cases.constraints) await runCase('constraints', c);
    for (const c of cases.transactions) await runCase('transactions', c);
    for (const c of cases.limits) await runCase('limits', c);
    const hdr = (notes) => ({
      oracle: 'production',
      target: `${PROJECT} (default): FIRESTORE_NATIVE, STANDARD, PESSIMISTIC, europe-west1; a second ruleset (rules below), deployed for this file only`,
      generator: GENERATOR,
      recordedAt: `${startedAt} .. ${new Date().toISOString()}`,
      versions: versions({ api: 'firestore.googleapis.com REST v1 and gRPC google.firestore.v1.Firestore; firebaserules.googleapis.com v1; identitytoolkit.googleapis.com v1', ...grpcVersions() }),
      notes: [
        'Auth: an end-user ID token (actor user); the owner only seeds. Each case is a runQuery under a rule "allow list: if <cond>" whose filters are related to the condition; outcome OK is allow, PERMISSION_DENIED is deny. A document the query matches exists in each case.',
        ...notes,
      ],
    });
    writeCorpus('f5-rules-constraints.json', hdr(['Doubles are sent as doubleValue (d:3 is 3.0, d:-0 is -0.0).']), { rules: { file: 'firestore.rules', source }, cases: files.constraints });
    writeCorpus('f5-rules-lookup-limits.json', hdr(['Lookups of one document in a request are cached and counted once, so every limit case uses disjoint document ranges (see the first recording, f5-rules-lookups.json, whose g10/g11 cases share documents).']), { rules: { file: 'firestore.rules', source }, cases: files.limits });
    writeCorpus('f5-rules-transactions.json', hdr(['Transactions that begin with new_transaction on the first read (BeginTransaction is refused to end users, see begin.*); the rules are those of the rules argument, a superset of f5-rules-deployed.json.']), { rules: { file: 'firestore.rules', source }, cases: files.transactions });
  } finally {
    try {
      await as('owner');
      const del = [...written].map((n) => ({ delete: n }));
      for (let i = 0; i < del.length; i += 200) await rest('POST', `${DB}/documents:commit`, { writes: del.slice(i, i + 200) });
      const left = [];
      for (const cid of new Set([...written].map((n) => rel(n).split('/')[0]))) {
        const r = await rest('GET', `${DB}/documents/${cid}?showMissing=true&pageSize=300`);
        for (const d of r.body.documents || []) left.push(rel(d.name));
      }
      log(`left after cleanup: ${left.length}\n`);
    } catch (e) {
      log(`cleanup failed: ${e.message}\n`);
    }
    try {
      const r = await rulesCall('PATCH', `projects/${PROJECT}/releases/cloud.firestore`, { release: { name: `projects/${PROJECT}/releases/cloud.firestore`, rulesetName: ORIGINAL_RULESET } });
      log(`restore release: HTTP ${r.http}\n`);
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
  }
}

main().catch((e) => {
  log(`\nFAILED: ${e.message}\n`);
  process.exit(1);
});
