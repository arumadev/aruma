// Drives the Cloud Storage rules runtime jar (cloud-storage-rules-runtime,
// shipped with firebase-tools) as a black box, through the same newline
// separated JSON protocol on stdin/stdout that firebase-tools' Storage
// emulator uses (lib/emulator/storage/rules/runtime.js). Nothing inside the
// jar is read or decompiled.
//
// STORAGE_RULES_JAR overrides the jar path; JAVA the java binary.
import { spawn } from 'node:child_process';
import { homedir } from 'node:os';
import { join } from 'node:path';

export const JAR =
  process.env.STORAGE_RULES_JAR || join(homedir(), '.cache/firebase/emulators/cloud-storage-rules-runtime-v1.1.3.jar');
const JAVA = process.env.JAVA || '/opt/homebrew/opt/openjdk@21/bin/java';

// toValue converts a test-case JSON value the way firebase-tools does
// (whole numbers become ints), with two additions: {"__timestamp": iso}
// and {"__path": "/a/b"} for typed values.
export function toValue(v) {
  if (v === null || v === undefined) return { null_value: null };
  if (typeof v === 'string') return { string_value: v };
  if (typeof v === 'boolean') return { bool_value: v };
  if (typeof v === 'number') return Number.isInteger(v) ? { int_value: v } : { float_value: v };
  if (Array.isArray(v)) return { list_value: { values: v.map(toValue) } };
  if (typeof v === 'object') {
    if ('__timestamp' in v) return { timestamp_value: v.__timestamp };
    if ('__path' in v) {
      return { path_value: { segments: v.__path.split('/').filter((s) => s).map((simple) => ({ simple })) } };
    }
    const fields = {};
    for (const [k, x] of Object.entries(v)) fields[k] = toValue(x);
    return { map_value: { fields } };
  }
  throw new Error(`cannot convert ${typeof v}`);
}

export class StorageRuntime {
  constructor() {
    this.next = 0;
    this.pending = new Map();
    this.buf = '';
    this.rulesets = 0;
  }

  start() {
    this.proc = spawn(JAVA, ['-Duser.language=en', '-Duser.country=US', '-jar', JAR, 'serve'], {
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    this.stderr = '';
    this.proc.stderr.on('data', (b) => (this.stderr += b.toString()));
    const ready = new Promise((resolve) => this.pending.set(-1, resolve));
    this.proc.stdout.on('data', (b) => {
      this.buf += b.toString('utf8');
      let i;
      while ((i = this.buf.indexOf('\n')) >= 0) {
        const line = this.buf.slice(0, i).trim();
        this.buf = this.buf.slice(i + 1);
        if (!line) continue;
        let msg;
        try {
          msg = JSON.parse(line);
        } catch {
          continue;
        }
        const id = msg.id ?? msg.server_request_id;
        const h = this.pending.get(id);
        if (h) {
          this.pending.delete(id);
          h(msg);
        }
      }
    });
    return ready;
  }

  send(req, id) {
    const rid = id ?? this.next++;
    return new Promise((resolve) => {
      this.pending.set(rid, resolve);
      this.proc.stdin.write(JSON.stringify({ ...req, id: rid }) + '\n');
    });
  }

  async load(source) {
    const name = String(this.rulesets++);
    const res = await this.send({ action: 'load_ruleset', context: { rulesetName: name, source: { files: [{ name: 'storage.rules', content: source }] } } });
    return { name, res };
  }

  // verify evaluates one request. firestore(path) answers firestore.get /
  // firestore.exists lookups: it returns document fields (typed) or null.
  async verify(rulesetName, { path, method, delimiter, variables }, firestore) {
    let res = await this.send({
      action: 'verify',
      context: { rulesetName, service: 'firebase.storage', path, method, delimiter, variables },
    });
    const lookups = [];
    while (res && 'context' in res) {
      lookups.push(res.context.path);
      const doc = firestore ? firestore(res.context.path) : null;
      const reply = doc
        ? { result: { name: `projects/demo-oracle${res.context.path}`, fields: doc }, status: 'ok', warnings: [], errors: [] }
        : { status: 'not_found', warnings: [], errors: [] };
      res = await this.send(reply, res.server_request_id);
    }
    return { res, lookups };
  }

  stop() {
    return new Promise((resolve) => {
      if (!this.proc || this.proc.exitCode !== null) return resolve();
      this.proc.on('exit', () => resolve());
      this.proc.kill('SIGINT');
    });
  }
}
