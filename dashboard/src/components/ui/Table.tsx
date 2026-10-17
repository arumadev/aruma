import type { ReactNode } from 'react';
import styles from './Table.module.css';

/**
 * A data table with native table semantics. It scrolls sideways inside its own region on
 * narrow screens instead of the page. `caption` names it for assistive technology.
 */
export function Table({ caption, columns, children }: { caption: string; columns: string[]; children: ReactNode }) {
  return (
    <div className={styles.scroller} role="region" aria-label={caption} tabIndex={0}>
      <table className={styles.table}>
        <caption className={styles.caption}>{caption}</caption>
        <thead>
          <tr>
            {columns.map((c, i) => (
              <th key={i} scope="col" className={styles.th}>
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export function Row({ children }: { children: ReactNode }) {
  return <tr className={styles.row}>{children}</tr>;
}

export function Cell({ children, actions, nowrap }: { children?: ReactNode; actions?: boolean; nowrap?: boolean }) {
  return <td className={[styles.td, actions && styles.actions, nowrap && styles.nowrap].filter(Boolean).join(' ')}>{children}</td>;
}
