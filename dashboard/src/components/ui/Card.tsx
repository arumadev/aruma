import type { ReactNode } from 'react';
import styles from './Card.module.css';

/** An M3 outlined card holding one section of a page. */
export function Card({
  title,
  description,
  actions,
  children,
  as: Tag = 'section',
  tone,
}: {
  title?: string;
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  as?: 'section' | 'div' | 'li';
  tone?: 'danger';
}) {
  return (
    <Tag className={[styles.card, tone && styles[tone]].filter(Boolean).join(' ')}>
      {(title || actions) && (
        <header className={styles.header}>
          <div className={styles.heading}>
            {title && <h2 className={styles.title}>{title}</h2>}
            {description && <p className={styles.description}>{description}</p>}
          </div>
          {actions && <div className={styles.actions}>{actions}</div>}
        </header>
      )}
      {children}
    </Tag>
  );
}
