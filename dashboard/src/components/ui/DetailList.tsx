import type { ReactNode } from 'react';
import { CopyButton, Ltr } from './Code';
import styles from './DetailList.module.css';

/** Label and value pairs, such as a project's identifiers. */
export function DetailList({ children }: { children: ReactNode }) {
  return <dl className={styles.list}>{children}</dl>;
}

/**
 * One pair. Pass `value` for an identifier (shown left to right, with a copy button when
 * `copyLabel` is given) or `children` for anything else.
 */
export function Detail({ label, value, copyLabel, children }: { label: string; value?: string; copyLabel?: string; children?: ReactNode }) {
  return (
    <div className={styles.item}>
      <dt className={styles.label}>{label}</dt>
      <dd className={styles.value}>
        {value !== undefined ? <Ltr>{value}</Ltr> : children}
        {value !== undefined && copyLabel && <CopyButton value={value} label={copyLabel} />}
      </dd>
    </div>
  );
}
