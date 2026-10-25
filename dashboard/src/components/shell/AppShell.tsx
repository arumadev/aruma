'use client';

import { usePathname, useSearchParams } from 'next/navigation';
import { useState, type ReactNode } from 'react';
import { Dialog, Modal, ModalOverlay } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { IconButton } from '@/components/ui/Button';
import styles from './AppShell.module.css';
import { navItems, normalizePath } from './navigation';
import { SidePanel } from './SidePanel';
import { TopAppBar } from './TopAppBar';

/**
 * The signed-in layout: the navigation column beside the page on wide screens, or in a modal
 * drawer on narrow ones, and the top app bar above the page.
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
      <aside className={styles.side}>
        <div className={styles.sideContent}>
          <SidePanel projectId={projectId} items={items} current={pathname} />
        </div>
      </aside>
      <div className={styles.column}>
        <TopAppBar onOpenDrawer={() => setDrawerOpen(true)} />
        <main id="main" className={styles.main} tabIndex={-1}>
          {children}
        </main>
      </div>
      <ModalOverlay isOpen={drawerOpen} onOpenChange={setDrawerOpen} isDismissable className={styles.scrim}>
        <Modal className={styles.drawer}>
          <Dialog aria-label={t('navigation')} className={styles.drawerDialog}>
            <SidePanel
              projectId={projectId}
              items={items}
              current={pathname}
              onNavigate={() => setDrawerOpen(false)}
              headerAction={<IconButton icon="close" label={t('closeNavigation')} onPress={() => setDrawerOpen(false)} />}
            />
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
      <div className={styles.side} />
      <div className={styles.column}>
        <div className={styles.placeholderBar} />
        <main className={styles.main}>{children}</main>
      </div>
    </div>
  );
}
