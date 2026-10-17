import type { ReactNode } from 'react';
import styles from './EmptyState.module.css';
import { Icon, type IconName } from './Icon';

/** What a list shows when it has nothing yet, with the action that fills it. */
export function EmptyState({ icon, title, children, action }: { icon: IconName; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className={styles.empty}>
      <span className={styles.icon}>
        <Icon name={icon} size={40} />
      </span>
      <p className={styles.title}>{title}</p>
      {children && <p className={styles.text}>{children}</p>}
      {action}
    </div>
  );
}
