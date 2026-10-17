'use client';

import type { ReactNode } from 'react';
import { Footer } from '@/components/shell/Footer';
import { LocaleSwitch } from '@/components/shell/LocaleSwitch';
import { Wordmark } from '@/components/shell/Wordmark';
import styles from './AuthFrame.module.css';

/** The signed-out layout: the wordmark, one centered card, the language switch and the footer. */
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
      <Footer />
    </div>
  );
}
