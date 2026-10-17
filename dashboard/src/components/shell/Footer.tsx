'use client';

import { useTranslations } from 'use-intl';
import styles from './Footer.module.css';

/** Shown on every page. */
export function Footer() {
  const t = useTranslations('footer');
  return (
    <footer className={styles.footer}>
      <p>{t('disclaimer')}</p>
    </footer>
  );
}
