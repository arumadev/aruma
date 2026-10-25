'use client';

import type { ReactNode } from 'react';
import { LocaleSwitch } from '@/components/shell/LocaleSwitch';
import { Wordmark } from '@/components/shell/Wordmark';
import styles from './AuthFrame.module.css';

/** The signed-out layout: the wordmark, one centered card and the language switch. */
export function AuthFrame({ children }: { children: ReactNode }) {
  return (
    <div className={styles.frame}>
      <header className={styles.top}>
        <LocaleSwitch />
      </header>
      <main className={styles.main}>
        <Wordmark large />
        <div className={styles.card}>{children}</div>
      </main>
    </div>
  );
}
