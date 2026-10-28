'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { IconButton } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import styles from './Indexes.module.css';
import { pageOf } from './indexes';

/** "Items per page" and the page's rows of a list, the footer the Firebase console's tables have. */
export function usePager<T>(items: T[], initialSize: number) {
  const [size, setSize] = useState(initialSize);
  const [page, setPage] = useState(0);
  return { ...pageOf(items, page, size), size, setSize: (n: number) => (setSize(n), setPage(0)), setPage, total: items.length };
}

export function Pager({ pager }: { pager: ReturnType<typeof usePager> }) {
  const t = useTranslations('documents.indexes.pager');
  if (pager.total <= 10) return null;
  return (
    <div className={styles.pager}>
      <Select
        label={t('perPage')}
        value={String(pager.size)}
        onChange={(id) => pager.setSize(Number(id))}
        options={[10, 25, 50].map((n) => ({ id: String(n), label: String(n) }))}
      />
      <span aria-live="polite">{t('page', { page: pager.page + 1, pages: pager.pages })}</span>
      <IconButton icon="chevronForward" label={t('previous')} isDisabled={pager.page === 0} onPress={() => pager.setPage(pager.page - 1)} className={styles.flip} />
      <IconButton icon="chevronForward" label={t('next')} isDisabled={pager.page >= pager.pages - 1} onPress={() => pager.setPage(pager.page + 1)} />
    </div>
  );
}
