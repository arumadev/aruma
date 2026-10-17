'use client';

import { useEffect } from 'react';
import { useTranslations } from 'use-intl';

/** Names the browser tab after the page, in the current language. */
export function useDocumentTitle(title: string | undefined): void {
  const t = useTranslations('brand');
  const brand = t('name');
  useEffect(() => {
    document.title = title ? `${title} · ${brand}` : brand;
  }, [title, brand]);
}
