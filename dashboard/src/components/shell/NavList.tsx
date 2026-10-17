'use client';

import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Icon } from '@/components/ui/Icon';
import type { NavItem } from './navigation';
import styles from './NavList.module.css';

/**
 * The destinations, as a rail (icon over a short label) on wide screens or as drawer rows on
 * narrow ones. The current page carries aria-current="page".
 */
export function NavList({
  items,
  current,
  variant,
  onNavigate,
}: {
  items: NavItem[];
  current: string;
  variant: 'rail' | 'drawer';
  onNavigate?: () => void;
}) {
  const t = useTranslations('nav');
  return (
    <ul className={variant === 'rail' ? styles.rail : styles.drawer}>
      {items.map((item) => {
        const active = item.paths.includes(current);
        return (
          <li key={item.id}>
            <Link href={item.href} aria-current={active ? 'page' : undefined} className={styles.item} onPress={onNavigate}>
              <span className={styles.indicator}>
                <Icon name={item.icon} />
              </span>
              <span className={styles.label}>{t(item.label)}</span>
            </Link>
          </li>
        );
      })}
    </ul>
  );
}
