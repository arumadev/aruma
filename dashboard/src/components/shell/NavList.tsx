'use client';

import { useId } from 'react';
import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Icon } from '@/components/ui/Icon';
import type { NavItem } from './navigation';
import styles from './NavList.module.css';

/**
 * The destinations as rows with an icon and a label, split under headings by their group. The
 * current page carries aria-current="page".
 */
export function NavList({ items, current, onNavigate }: { items: NavItem[]; current: string; onNavigate?: () => void }) {
  const t = useTranslations('nav');
  const id = useId();
  const sections: { group?: NavItem['group']; items: NavItem[] }[] = [];
  for (const item of items) {
    const last = sections.at(-1);
    if (last && last.group === item.group) last.items.push(item);
    else sections.push({ group: item.group, items: [item] });
  }
  return (
    <div className={styles.sections}>
      {sections.map((section, i) => {
        const headingId = `${id}-${i}`;
        return (
          <div key={headingId} className={styles.section}>
            {section.group && (
              <span id={headingId} className={styles.heading}>
                {t(section.group)}
              </span>
            )}
            <ul className={styles.list} aria-labelledby={section.group ? headingId : undefined}>
              {section.items.map((item) => {
                const active = item.paths.includes(current);
                return (
                  <li key={item.id}>
                    <Link href={item.href} aria-current={active ? 'page' : undefined} className={styles.item} onPress={onNavigate}>
                      <Icon name={item.icon} size={20} />
                      <span className={styles.label}>{t(item.label)}</span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        );
      })}
    </div>
  );
}
