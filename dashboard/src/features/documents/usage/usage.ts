// The Usage tab's helpers: the periods, and the hourly rows the server counts shaped into the
// points a chart draws.
import type { UsagePeriod, UsageReport, UsageRow } from '../api';

/** The Firebase console's periods that a self-hosted server can answer. */
export const PERIODS: UsagePeriod[] = ['60m', '24h', '7d', '30d'];

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

export interface Point {
  /** The bucket's start, ms since the epoch (UTC). */
  at: number;
  row: UsageRow;
}

export const emptyRow = (): UsageRow => ({ reads: 0, writes: 0, deletes: 0, listenersPeak: 0, connectionsPeak: 0, rulesAllows: 0, rulesDenies: 0, rulesErrors: 0 });

/** Counts add; peaks keep the greater. */
export function merge(a: UsageRow, b: UsageRow): UsageRow {
  return {
    reads: a.reads + b.reads,
    writes: a.writes + b.writes,
    deletes: a.deletes + b.deletes,
    listenersPeak: Math.max(a.listenersPeak, b.listenersPeak),
    connectionsPeak: Math.max(a.connectionsPeak, b.connectionsPeak),
    rulesAllows: a.rulesAllows + b.rulesAllows,
    rulesDenies: a.rulesDenies + b.rulesDenies,
    rulesErrors: a.rulesErrors + b.rulesErrors,
  };
}

/** Whether a period's chart groups the hours into days: a week or a month is too many points. */
export const perDay = (period: UsagePeriod) => period === '7d' || period === '30d';

/**
 * One point per hour (per day for 7d and 30d) from the report's start to its end, an hour the
 * server has no row for counting zero, so a quiet hour is a gap in the data and not in the axis.
 */
export function points(report: UsageReport): Point[] {
  const step = perDay(report.period) ? DAY : HOUR;
  const start = Date.parse(report.start);
  const end = Date.parse(report.end);
  if (Number.isNaN(start) || Number.isNaN(end)) return [];
  // Days begin at midnight UTC; the first one may be partial.
  const first = step === DAY ? Math.floor(start / DAY) * DAY : start;
  const byBucket = new Map<number, UsageRow>();
  for (const row of report.hours) {
    if (!row.hour) continue;
    const at = Date.parse(row.hour);
    if (Number.isNaN(at)) continue;
    const bucket = Math.floor(at / step) * step;
    byBucket.set(bucket, merge(byBucket.get(bucket) ?? emptyRow(), row));
  }
  const out: Point[] = [];
  for (let at = first; at <= end; at += step) out.push({ at, row: byBucket.get(at) ?? emptyRow() });
  return out;
}

/** The greatest value, at least 1 so an empty chart still has a scale. */
export const ceiling = (values: number[]) => Math.max(1, ...values);

/** A round upper bound for an axis: 1, 2 or 5 times a power of ten. */
export function niceMax(max: number): number {
  if (max <= 1) return 1;
  const exp = Math.pow(10, Math.floor(Math.log10(max)));
  const f = max / exp;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * exp;
}
