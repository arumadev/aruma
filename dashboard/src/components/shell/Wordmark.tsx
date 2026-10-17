'use client';

import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import styles from './Wordmark.module.css';

/** The temporary logo: the product name set in the brand colour. */
export function Wordmark({ href, large }: { href?: string; large?: boolean }) {
  const t = useTranslations('brand');
  const className = [styles.wordmark, large && styles.large].filter(Boolean).join(' ');
  if (!href) return <span className={className}>{t('name')}</span>;
  return (
    <Link href={href} className={className}>
      {t('name')}
    </Link>
  );
}
