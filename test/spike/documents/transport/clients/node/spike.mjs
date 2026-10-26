// firebase-admin (with @google-cloud/firestore under it) against the transport spike server
// (../../server): a unary call (Commit through DocumentReference.set), the index error's status
// details, a BatchGetDocuments (get), a Listen stream held until a later message arrives
// (onSnapshot), and a Write handshake through the generated client (v1.FirestoreClient), which
// the Admin API does not expose. One JSON summary line on stdout; exit status 1 on any failure.
//
//   node spike.mjs --addr 127.0.0.1:9100 --auth emulator
//   node spike.mjs --addr 127.0.0.1:9102 --ssl --ca ../../.generated/ca.pem --auth sa --sa ../../.generated/sa/sa-google.json [--prefer-rest]
//
// Run it under the repository's network guard (run.sh does):
//   NODE_OPTIONS="--require test/experiments/netguard.cjs" ARUMA_NETGUARD_LOG=…
// and, for TLS with the spike's CA, NODE_EXTRA_CA_CERTS (REST, the token endpoint) and
// GRPC_DEFAULT_SSL_ROOTS_FILE_PATH (grpc-js, which ignores NODE_EXTRA_CA_CERTS).
import fs from 'node:fs';
import { parseArgs } from 'node:util';
import { initializeApp, cert } from 'firebase-admin/app';
import { getFirestore } from 'firebase-admin/firestore';
import gcf from '@google-cloud/firestore';
import gax from 'google-gax';

const { v1 } = gcf;

const { values: args } = parseArgs({
  options: {
    addr: { type: 'string', default: '127.0.0.1:9100' },
    ssl: { type: 'boolean', default: false },
    ca: { type: 'string' },
    auth: { type: 'string', default: 'emulator' }, // emulator | sa
    sa: { type: 'string' },
    'prefer-rest': { type: 'boolean', default: false },
    hold: { type: 'string', default: '60' }, // seconds the stream must have been open at the later message
    'listen-for': { type: 'string', default: '90' },
    label: { type: 'string', default: '' },
  },
});
const project = 'spike-project';
const database = `projects/${project}/databases/(default)`;
const [host, port] = args.addr.split(':');

if (args.auth === 'emulator') process.env.FIRESTORE_EMULATOR_HOST = args.addr;
const key = args.auth === 'sa' ? JSON.parse(fs.readFileSync(args.sa, 'utf8')) : undefined;
const app = initializeApp(key ? { credential: cert(key), projectId: project } : { projectId: project });
const db = getFirestore(app);
if (args.auth !== 'emulator') db.settings({ host: args.addr, ssl: args.ssl, preferRest: args['prefer-rest'] });
else if (args['prefer-rest']) db.settings({ preferRest: true });

async function result(f) {
  try {
    await f();
    return 'PASS';
  } catch (e) {
    return `FAIL: ${e?.message ?? e}`;
  }
}

const summary = { client: 'node', label: args.label, preferRest: args['prefer-rest'] };

summary.unary = await result(() => db.doc('spike/unary').set({ n: 1 }));

summary.index_error = await result(async () => {
  try {
    await db.doc('spike/index-error').set({ n: 1 });
  } catch (e) {
    if (e.code !== 9) throw new Error(`code ${e.code}: ${e.message}`);
    // gax decodes grpc-status-details-bin into statusDetails (REST: the error body's details).
    const details = (e.statusDetails ?? []).map((d) => d?.constructor?.name ?? typeof d);
    summary.index_error_details = details;
    if (!e.message.includes('https://console.firebase.google.com/')) throw new Error(`no link in ${e.message}`);
    return;
  }
  throw new Error('no error');
});

summary.batch_get = await result(async () => {
  const snap = await db.doc('spike/doc').get();
  if (snap.exists) throw new Error('the document exists');
});

summary.write = await result(async () => {
  if (args['prefer-rest']) {
    summary.write_note = 'Write is a bidirectional stream: gRPC only, as Listen; checked in the gRPC runs';
    return;
  }
  // As @google-cloud/firestore builds its own client: insecure channel credentials for ssl=false
  // (gax then sends no call credentials), else gax's TLS with the key's credentials, the CA
  // coming from GRPC_DEFAULT_SSL_ROOTS_FILE_PATH. Passing sslCreds would drop the credentials.
  const opts = { servicePath: host, port: Number(port), projectId: project };
  if (!args.ssl) opts.sslCreds = gax.grpc.credentials.createInsecure();
  if (key) opts.credentials = { client_email: key.client_email, private_key: key.private_key };
  const client = new v1.FirestoreClient(opts);
  const stream = client.write();
  const responses = [];
  const waiters = [];
  stream.on('data', (r) => (waiters.length ? waiters.shift().resolve(r) : responses.push(r)));
  stream.on('error', (e) => waiters.splice(0).forEach((w) => w.reject(e)));
  const next = () =>
    responses.length ? Promise.resolve(responses.shift()) : new Promise((resolve, reject) => waiters.push({ resolve, reject }));
  stream.write({ database });
  const hs = await next();
  if (!hs.streamToken?.length || hs.writeResults?.length) throw new Error(`handshake ${JSON.stringify(hs)}`);
  stream.write({ streamToken: hs.streamToken, writes: [{ delete: `${database}/documents/spike/unary` }] });
  const res = await next();
  if (res.writeResults?.length !== 1 || !res.commitTime) throw new Error(`write ${JSON.stringify(res)}`);
  stream.end();
  await client.close();
});

summary.stream = await result(
  () =>
    new Promise((resolve, reject) => {
      const begin = Date.now();
      let n = 0;
      const timer = setTimeout(() => {
        unsubscribe();
        reject(new Error(`no later snapshot after ${args['listen-for']} s (${n} snapshots)`));
      }, Number(args['listen-for']) * 1000);
      const unsubscribe = db.doc('spike/doc').onSnapshot(
        (snap) => {
          n++;
          if (n === 1) {
            if (snap.exists) reject(new Error('first snapshot: the document exists'));
            return;
          }
          clearTimeout(timer);
          unsubscribe();
          const held = (Date.now() - begin) / 1000;
          process.stderr.write(`later snapshot after ${held.toFixed(0)} s on the same stream\n`);
          if (!snap.exists || snap.get('later') !== true) reject(new Error(`later snapshot ${JSON.stringify(snap.data())}`));
          else if (held < Number(args.hold)) reject(new Error(`later snapshot after ${held} s, before ${args.hold} s`));
          else resolve();
        },
        (e) => {
          clearTimeout(timer);
          reject(e);
        },
      );
    }),
);

console.log(JSON.stringify(summary));
await db.terminate();
process.exit(['unary', 'index_error', 'batch_get', 'write', 'stream'].every((k) => summary[k] === 'PASS') ? 0 : 1);
