'use client';

import { usePathname, useSearchParams } from 'next/navigation';
import { useState, type ReactNode } from 'react';
import { Dialog, Modal, ModalOverlay } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { IconButton } from '@/components/ui/Button';
import { routes } from '@/lib/routes';
import styles from './AppShell.module.css';
import { Footer } from './Footer';
import { NavList } from './NavList';
import { navItems, normalizePath } from './navigation';
import { TopAppBar } from './TopAppBar';
import { Wordmark } from './Wordmark';

/**
 * The signed-in layout: top app bar, a navigation rail on wide screens or a modal drawer on
 * narrow ones, the page, and the footer.
 */
export function AppShell({ children }: { children: ReactNode }) {
  const t = useTranslations('shell');
  const pathname = normalizePath(usePathname());
  const search = useSearchParams();
  const projectId = pathname.startsWith('/project/') ? search.get('id') : null;
  const items = navItems(projectId);
  const [drawerOpen, setDrawerOpen] = useState(false);

  return (
    <div className={styles.shell}>
      <a href="#main" className={styles.skip}>
        {t('skipToContent')}
      </a>
      <TopAppBar projectId={projectId} onOpenDrawer={() => setDrawerOpen(true)} />
      <div className={styles.body}>
        <nav className={styles.rail} aria-label={t('navigation')}>
          <NavList items={items} current={pathname} variant="rail" />
        </nav>
        <div className={styles.column}>
          <main id="main" className={styles.main} tabIndex={-1}>
            {children}
          </main>
          <Footer />
        </div>
      </div>
      <ModalOverlay isOpen={drawerOpen} onOpenChange={setDrawerOpen} isDismissable className={styles.scrim}>
        <Modal className={styles.drawer}>
          <Dialog aria-label={t('navigation')} className={styles.drawerDialog}>
            <div className={styles.drawerHeader}>
              <Wordmark href={routes.projects} />
              <IconButton icon="close" label={t('closeNavigation')} onPress={() => setDrawerOpen(false)} />
            </div>
            <nav aria-label={t('navigation')}>
              <NavList items={items} current={pathname} variant="drawer" onNavigate={() => setDrawerOpen(false)} />
            </nav>
          </Dialog>
        </Modal>
      </ModalOverlay>
    </div>
  );
}

/** What the shell shows before the session is known, with an optional message. */
export function ShellPlaceholder({ children }: { children?: ReactNode }) {
  return (
    <div className={styles.shell}>
      <div className={styles.placeholderBar} />
      <div className={styles.body}>
        <div className={styles.rail} />
        <div className={styles.column}>
          <main className={styles.main}>{children}</main>
          <Footer />
        </div>
      </div>
    </div>
  );
}
