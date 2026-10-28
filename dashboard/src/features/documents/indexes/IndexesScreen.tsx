'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Tabs } from '@/components/ui/Tabs';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { AutomaticTab } from './AutomaticTab';
import { ManualTab } from './ManualTab';
import { TtlTab } from './TtlTab';

type Sub = 'manual' | 'automatic' | 'ttl';

/**
 * The database's indexes: Manual (the composite ones, which queries name), Automatic (the
 * single-field ones and the fields that differ), and the time-to-live policies.
 */
export function IndexesScreen() {
  const t = useTranslations('documents.indexes');
  const [selected, setSelected] = useState<Sub>('manual');
  useDocumentTitle(t('title'));

  return (
    <Tabs
      label={t('sections')}
      selected={selected}
      onSelect={(id) => setSelected(id as Sub)}
      items={[
        { id: 'manual', label: t('manual.tab'), content: <ManualTab /> },
        { id: 'automatic', label: t('automatic.tab'), content: <AutomaticTab /> },
        { id: 'ttl', label: t('ttl.tab'), content: <TtlTab /> },
      ]}
    />
  );
}
