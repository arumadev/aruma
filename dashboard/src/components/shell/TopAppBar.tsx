'use client';

import { useTranslations } from 'use-intl';
import { IconButton } from '@/components/ui/Button';
import { routes } from '@/lib/routes';
import { AccountMenu } from './AccountMenu';
import { LocaleSwitch } from './LocaleSwitch';
import { ProjectSwitcher } from './ProjectSwitcher';
import styles from './TopAppBar.module.css';
import { Wordmark } from './Wordmark';

export function TopAppBar({ projectId, onOpenDrawer }: { projectId: string | null; onOpenDrawer: () => void }) {
  const t = useTranslations('shell');
  return (
    <header className={styles.bar}>
      <IconButton icon="menu" label={t('openNavigation')} className={styles.menuButton} onPress={onOpenDrawer} />
      <Wordmark href={routes.projects} />
      {projectId && <ProjectSwitcher projectId={projectId} />}
      <div className={styles.end}>
        <LocaleSwitch />
        <AccountMenu />
      </div>
    </header>
  );
}
