'use client';

import { useTranslations } from 'use-intl';
import { IconButton } from '@/components/ui/Button';
import { routes } from '@/lib/routes';
import { AccountMenu } from './AccountMenu';
import { LocaleSwitch } from './LocaleSwitch';
import styles from './TopAppBar.module.css';
import { Wordmark } from './Wordmark';

/** The bar above the page: the language switch and the account menu, plus the menu button on narrow screens. */
export function TopAppBar({ onOpenDrawer }: { onOpenDrawer: () => void }) {
  const t = useTranslations('shell');
  return (
    <header className={styles.bar}>
      <IconButton icon="menu" label={t('openNavigation')} className={styles.menuButton} onPress={onOpenDrawer} />
      <span className={styles.wordmark}>
        <Wordmark href={routes.projects} />
      </span>
      <div className={styles.end}>
        <LocaleSwitch />
        <AccountMenu />
      </div>
    </header>
  );
}
