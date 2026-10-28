import type { Page, Route } from '@playwright/test';
import type { Field, Index, RuleIssue, SimulateRequest, UsageRow } from '../src/features/documents/api';

// The administration half of the mock Document Database console API (rules, indexes, exemptions,
// TTL and usage under /projects/chat-app/documents/…). Routes it does not own, the data
// browser's, fall through to whatever else answers them. State is fresh per test.

const PROJECT = 'chat-app';
const DB = `projects/${PROJECT}/databases/(default)`;
const NOW = Date.parse('2026-10-18T09:00:00Z');

export const LOCKED = `rules_version = '2';

service cloud.firestore {
  match /databases/{database}/documents {
    match /{document=**} {
      allow read, write: if false;
    }
  }
}
`;

export const SIGNED_IN = LOCKED.replace('if false', 'if request.auth != null');

interface Ruleset {
  id: string;
  createTime: string;
  source: string;
}

/** A request body, whichever route it is for. */
interface Body {
  source?: string;
  collectionGroup?: string;
  index?: Index;
  indexConfig?: { indexes: Index[] };
}

type Reply = { status: number; body: unknown };
const ok = (body: unknown): Reply => ({ status: 200, body });
const fail = (status: number, rpc: string, message: string, details?: unknown[]): Reply => ({
  status,
  body: { error: { code: status, status: rpc, message, ...(details && { details }) } },
});

/** What this mock's compiler refuses: unbalanced braces, and an `if` with no condition. */
export function check(source: string): RuleIssue[] {
  const issues: RuleIssue[] = [];
  const lines = source.split('\n');
  lines.forEach((text, i) => {
    const m = /if\s*;/.exec(text);
    if (m) {
      issues.push({
        severity: 'ERROR',
        description: "mismatched input ';' expecting {'!', '(', 'true', 'false'}",
        sourcePosition: { fileName: 'firestore.rules', line: i + 1, column: m.index + 3, currentOffset: 0, endOffset: 0 },
      });
    }
  });
  const open = (source.match(/{/g) ?? []).length;
  const close = (source.match(/}/g) ?? []).length;
  if (open !== close) {
    issues.push({
      severity: 'ERROR',
      description: "mismatched input '<EOF>' expecting '}'",
      sourcePosition: { fileName: 'firestore.rules', line: lines.length, column: 1, currentOffset: 0, endOffset: 0 },
    });
  }
  return issues;
}

export class MockDocumentsAdmin {
  rulesets: Ruleset[] = [{ id: 'r1', createTime: '2026-10-17T12:12:00Z', source: LOCKED }];
  current = 'r1';
  published = true;
  indexes: Index[] = [];
  fields: Field[] = [];
  ttl: Field[] = [];
  private lists = 0;
  private counter = 0;
  /** Every simulate request, for tests to inspect. */
  simulated: SimulateRequest[] = [];
  /** Every usage period asked for. */
  periods: string[] = [];

  async install(page: Page): Promise<void> {
    await page.route(/\/console\/v1\/projects\/[^/]+\/documents(\/|\?|:|$)/, (route) => this.handle(route));
  }

  private async handle(route: Route): Promise<void> {
    const req = route.request();
    const url = new URL(req.url());
    const sub = url.pathname.replace(/^\/console\/v1\/projects\/[^/]+\/documents/, '');
    const body = req.postData() ? (JSON.parse(req.postData()!) as Body) : {};
    const reply = this.dispatch(req.method(), sub, url.searchParams, body);
    if (!reply) return route.fallback();
    await route.fulfill({ status: reply.status, contentType: 'application/json', body: JSON.stringify(reply.body) });
  }

  private dispatch(method: string, sub: string, q: URLSearchParams, body: Body): Reply | null {
    if (sub === '' && method === 'GET') {
      return ok({
        enabled: true,
        databaseId: '(default)',
        name: DB,
        limits: { pageSize: 50, collectionsPageSize: 300, maxPageSize: 1000, documentBytes: 1048576, fieldDepth: 20, requestBytes: 1048576, recursiveDeleteDocs: 5000 },
      });
    }
    const route = `${method} ${sub}`;
    if (route === 'GET /rules') {
      const cur = this.rulesets.find((r) => r.id === this.current);
      return ok({ published: this.published, source: cur?.source ?? '', rulesetName: `projects/${PROJECT}/rulesets/${this.current}`, updateTime: cur?.createTime });
    }
    if (route === 'PUT /rules') {
      const issues = check(String(body.source));
      if (issues.some((i) => i.severity === 'ERROR')) return this.invalid(issues);
      const id = `r${this.rulesets.length + 1}`;
      const createTime = new Date(NOW + this.rulesets.length * 60_000).toISOString();
      this.rulesets.unshift({ id, createTime, source: String(body.source) });
      this.current = id;
      this.published = true;
      return ok({ published: true, source: body.source, rulesetName: `projects/${PROJECT}/rulesets/${id}`, updateTime: createTime });
    }
    if (route === 'POST /rules:validate') {
      const issues = check(String(body.source));
      return ok({ valid: !issues.some((i) => i.severity === 'ERROR'), issues });
    }
    if (route === 'POST /rules:simulate') {
      this.simulated.push(body as SimulateRequest);
      return this.simulate(body as SimulateRequest);
    }
    if (route === 'GET /rulesets') {
      return ok({ rulesets: this.rulesets.map((r) => ({ name: `projects/${PROJECT}/rulesets/${r.id}`, createTime: r.createTime, current: r.id === this.current })) });
    }
    const set = /^GET \/rulesets\/([^/]+)$/.exec(route);
    if (set) {
      const r = this.rulesets.find((x) => x.id === set[1]);
      return r ? ok({ name: `projects/${PROJECT}/rulesets/${r.id}`, createTime: r.createTime, source: r.source }) : fail(404, 'NOT_FOUND', 'No such ruleset.');
    }
    if (route === 'GET /indexes') {
      // An index that was just made is Building for one read, then Enabled.
      this.lists++;
      const out = this.indexes.map((i) => ({ ...i }));
      this.indexes.forEach((i) => {
        if (i.state === 'CREATING') i.state = 'READY';
      });
      return ok({ indexes: out });
    }
    if (route === 'POST /indexes') {
      const id = `ix${++this.counter}`;
      const index: Index = {
        ...(body.index as Index),
        name: `${DB}/collectionGroups/${body.collectionGroup}/indexes/${id}`,
        state: 'CREATING',
      };
      index.fields = [...index.fields, { fieldPath: '__name__', order: 'ASCENDING' }];
      this.indexes.push(index);
      return ok({ name: `${DB}/operations/op${this.counter}`, done: false });
    }
    const del = /^DELETE \/indexes\/([^/]+)\/([^/]+)$/.exec(route);
    if (del) {
      this.indexes = this.indexes.filter((i) => !i.name?.endsWith(`collectionGroups/${decodeURIComponent(del[1]!)}/indexes/${del[2]}`));
      return ok({});
    }
    if (route === 'GET /fields') return ok({ fields: this.fields });
    if (route === 'PUT /fields' || route === 'DELETE /fields') {
      const group = q.get('group')!;
      const field = q.get('field')!;
      const name = `${DB}/collectionGroups/${group}/fields/${field}`;
      this.fields = this.fields.filter((f) => f.name !== name);
      if (method === 'PUT') this.fields.push({ name, indexConfig: { usesAncestorConfig: false, indexes: body.indexConfig!.indexes } });
      return ok({ name: `${DB}/operations/op${++this.counter}`, done: true });
    }
    if (route === 'GET /ttl') return ok({ fields: this.ttl });
    if (route === 'PUT /ttl' || route === 'DELETE /ttl') {
      const name = `${DB}/collectionGroups/${q.get('group')}/fields/${q.get('field')}`;
      // Like the server (core/ttl.go), it takes any number of policies in a group: one per group is the console's rule.
      this.ttl = this.ttl.filter((f) => f.name !== name);
      if (method === 'PUT') this.ttl.push({ name, ttlConfig: { state: 'ACTIVE' } });
      return ok({ name: `${DB}/operations/op${++this.counter}`, done: true });
    }
    if (route === 'GET /usage') {
      const period = q.get('period') ?? '24h';
      this.periods.push(period);
      return ok(usage(period));
    }
    return null;
  }

  private invalid(issues: RuleIssue[]): Reply {
    return fail(400, 'INVALID_ARGUMENT', 'The rules do not compile.', [{ '@type': 'type.aruma.dev/documents.RulesIssues', issues }]);
  }

  private simulate(req: SimulateRequest): Reply {
    const source = req.source ?? this.rulesets.find((r) => r.id === this.current)?.source ?? LOCKED;
    const issues = check(source);
    if (issues.length > 0) return this.invalid(issues);
    const line = source.split('\n').findIndex((l) => l.includes('allow')) + 1;
    const allowed = /if\s+true/.test(source) || (/request\.auth\s*!=\s*null/.test(source) && Boolean(req.auth));
    return ok({ allowed, statements: [{ line, column: 7, methods: ['read', 'write'], outcome: allowed ? 'true' : 'false' }], issues: [] });
  }
}

const PERIOD_HOURS: Record<string, number> = { '60m': 2, '24h': 25, '7d': 169, '30d': 721 };

/** A usage report whose counts are a pattern the test can add up: reads are 2 an hour, writes 1. */
function usage(period: string) {
  const n = PERIOD_HOURS[period] ?? 25;
  const end = Math.floor(NOW / 3_600_000) * 3_600_000;
  const start = end - (n - 1) * 3_600_000;
  const hours: UsageRow[] = Array.from({ length: n }, (_, i) => ({
    hour: new Date(start + i * 3_600_000).toISOString(),
    reads: 2,
    writes: 1,
    deletes: i === n - 1 ? 3 : 0,
    listenersPeak: i % 5,
    connectionsPeak: i % 3,
    rulesAllows: 4,
    rulesDenies: 1,
    rulesErrors: 0,
  }));
  const sum = (k: 'reads' | 'writes' | 'deletes' | 'rulesAllows' | 'rulesDenies' | 'rulesErrors') => hours.reduce((a, h) => a + h[k], 0);
  return {
    period,
    start: new Date(start).toISOString(),
    end: new Date(NOW).toISOString(),
    granularity: 'hour',
    totals: {
      reads: sum('reads'),
      writes: sum('writes'),
      deletes: sum('deletes'),
      listenersPeak: Math.max(...hours.map((h) => h.listenersPeak)),
      connectionsPeak: Math.max(...hours.map((h) => h.connectionsPeak)),
      rulesAllows: sum('rulesAllows'),
      rulesDenies: sum('rulesDenies'),
      rulesErrors: sum('rulesErrors'),
    },
    hours,
  };
}
