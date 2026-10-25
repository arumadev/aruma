'use client';

import type { ReactNode } from 'react';
import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Icon } from '@/components/ui/Icon';
import { routes } from '@/lib/routes';
import { NavList } from './NavList';
import type { NavItem } from './navigation';
import { ProjectSwitcher } from './ProjectSwitcher';
import styles from './SidePanel.module.css';
import { Wordmark } from './Wordmark';

/**
 * The navigation column: the wordmark, then inside a project a way back to the projects and the
 * project switcher, then the destinations. Shown beside the page on wide screens and in a drawer
 * on narrow ones.
 */
export function SidePanel({
  projectId,
  items,
  current,
  onNavigate,
  headerAction,
}: {
  projectId: string | null;
  items: NavItem[];
  current: string;
  onNavigate?: () => void;
  /** Shown at the end of the wordmark row, such as the drawer's close button. */
  headerAction?: ReactNode;
}) {
  const t = useTranslations('nav');
  const shell = useTranslations('shell');
  const back = projectId ? items.find((item) => item.id === 'projects') : undefined;
  const destinations = back ? items.filter((item) => item !== back) : items;
  return (
    <div className={styles.panel}>
      <div className={styles.brand}>
        <Wordmark href={routes.projects} />
        {headerAction}
      </div>
      <nav aria-label={shell('navigation')} className={styles.nav}>
        {back && projectId && (
          <div className={styles.project}>
            <Link href={back.href} className={styles.back} onPress={onNavigate}>
              <Icon name="arrowBack" size={18} />
              <span>{t(back.label)}</span>
            </Link>
            <ProjectSwitcher projectId={projectId} />
          </div>
        )}
        <NavList items={destinations} current={current} onNavigate={onNavigate} />
      </nav>
    </div>
  );
}
