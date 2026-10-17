import styles from './Badge.module.css';

export type BadgeTone = 'neutral' | 'primary' | 'warning' | 'error';

/** A small status label, such as "Deleted" or "Disabled". */
export function Badge({ tone = 'neutral', children }: { tone?: BadgeTone; children: string }) {
  return <span className={`${styles.badge} ${styles[tone]}`}>{children}</span>;
}
