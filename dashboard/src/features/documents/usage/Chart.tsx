'use client';

import { useFormatter } from 'use-intl';
import styles from './Chart.module.css';
import { niceMax, type Point } from './usage';

export interface Series {
  id: string;
  label: string;
  /** The colour token and the dash pattern, so two series differ by more than hue. */
  color: string;
  dash?: string;
  value: (p: Point) => number;
}

const W = 640;
const H = 180;
const PAD = { top: 16, right: 8, bottom: 22, left: 8 };

/**
 * A line per series over the period's points: plain SVG, a legend naming each line with its
 * total, and an accessible name that gives the same totals. The chart is left to right in both
 * languages (time runs that way), its labels set apart from the surrounding text.
 */
export function LineChart({ title, summary, points, series, perDay }: { title: string; summary: string; points: Point[]; series: Series[]; perDay: boolean }) {
  const format = useFormatter();
  const max = niceMax(Math.max(0, ...series.flatMap((s) => points.map(s.value))));
  const n = Math.max(points.length - 1, 1);
  const x = (i: number) => PAD.left + (i / n) * (W - PAD.left - PAD.right);
  const y = (v: number) => PAD.top + (1 - v / max) * (H - PAD.top - PAD.bottom);
  const label = (at: number) => format.dateTime(new Date(at), perDay ? { month: 'short', day: 'numeric', timeZone: 'UTC' } : { hour: 'numeric', minute: '2-digit', timeZone: 'UTC' });
  const ticks = points.length > 0 ? [0, Math.floor((points.length - 1) / 2), points.length - 1] : [];

  return (
    <figure className={styles.figure}>
      <figcaption className={styles.title}>{title}</figcaption>
      <svg className={styles.svg} viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`${title}. ${summary}`}>
        {[0, 0.5, 1].map((f) => (
          <line key={f} className={styles.grid} x1={PAD.left} x2={W - PAD.right} y1={y(max * f)} y2={y(max * f)} />
        ))}
        <text className={styles.tick} x={PAD.left} y={PAD.top - 5}>
          {format.number(max)}
        </text>
        {series.map((s) => (
          <polyline
            key={s.id}
            className={styles.line}
            data-series={s.id}
            points={points.map((p, i) => `${x(i).toFixed(1)},${y(s.value(p)).toFixed(1)}`).join(' ')}
            stroke={s.color}
            strokeDasharray={s.dash}
          />
        ))}
        {ticks.map((i) => (
          <text key={i} className={styles.tick} x={x(i)} y={H - 6} textAnchor={i === 0 ? 'start' : i === points.length - 1 ? 'end' : 'middle'}>
            {label(points[i]!.at)}
          </text>
        ))}
      </svg>
      <ul className={styles.legend}>
        {series.map((s) => (
          <li key={s.id}>
            <svg width="24" height="8" aria-hidden="true">
              <line x1="0" x2="24" y1="4" y2="4" stroke={s.color} strokeWidth="3" strokeDasharray={s.dash} />
            </svg>
            <span>{s.label}</span>
          </li>
        ))}
      </ul>
    </figure>
  );
}
