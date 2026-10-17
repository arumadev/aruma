'use client';

import { useTranslations } from 'use-intl';
import styles from './Skeleton.module.css';

/** Placeholder blocks shown while a page loads. Announced once as "Loading". */
export function Skeleton({ lines = 3, blocks = 0 }: { lines?: number; blocks?: number }) {
  const t = useTranslations('common');
  return (
    <div className={styles.skeleton} role="status" aria-label={t('loading')}>
      <span className={`${styles.bone} ${styles.heading}`} />
      {Array.from({ length: lines }, (_, i) => (
        <span key={`l${i}`} className={styles.bone} />
      ))}
      {blocks > 0 && (
        <div className={styles.blocks}>
          {Array.from({ length: blocks }, (_, i) => (
            <span key={`b${i}`} className={`${styles.bone} ${styles.block}`} />
          ))}
        </div>
      )}
    </div>
  );
}
