'use client';

import type { ReactNode } from 'react';
import { Tab, TabList, TabPanel, Tabs as AriaTabs } from 'react-aria-components';
import styles from './Tabs.module.css';

export interface TabItem {
  id: string;
  label: string;
  content: ReactNode;
}

/** M3 primary tabs. Arrow keys follow the reading direction. */
export function Tabs({ label, items, selected, onSelect }: { label: string; items: TabItem[]; selected?: string; onSelect?: (id: string) => void }) {
  return (
    <AriaTabs className={styles.tabs} selectedKey={selected} onSelectionChange={onSelect && ((key) => onSelect(String(key)))}>
      <TabList aria-label={label} className={styles.list}>
        {items.map((item) => (
          <Tab key={item.id} id={item.id} className={styles.tab}>
            {item.label}
          </Tab>
        ))}
      </TabList>
      {items.map((item) => (
        <TabPanel key={item.id} id={item.id} className={styles.panel}>
          {item.content}
        </TabPanel>
      ))}
    </AriaTabs>
  );
}
