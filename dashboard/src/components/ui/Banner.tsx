import type { ReactNode } from 'react';
import styles from './Banner.module.css';
import { Icon, type IconName } from './Icon';

export type BannerTone = 'info' | 'warning' | 'error';

const ICONS: Record<BannerTone, IconName> = { info: 'info', warning: 'warning', error: 'error' };

/**
 * An inline message. Errors are announced at once (role="alert"); other tones are part of the
 * page and read in order.
 */
export function Banner({ tone = 'info', title, children, action }: { tone?: BannerTone; title?: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className={`${styles.banner} ${styles[tone]}`} role={tone === 'error' ? 'alert' : undefined}>
      <Icon name={ICONS[tone]} />
      <div className={styles.body}>
        {title && <p className={styles.title}>{title}</p>}
        {children && <div className={styles.text}>{children}</div>}
      </div>
      {action && <div className={styles.action}>{action}</div>}
    </div>
  );
}
