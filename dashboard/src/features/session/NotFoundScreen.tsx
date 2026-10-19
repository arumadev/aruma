'use client';

import { useTranslations } from 'use-intl';
import { LinkButton } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { routes } from '@/lib/routes';
import { AuthFrame } from './AuthFrame';

/** Any path the dashboard does not have. */
export function NotFoundScreen() {
  const t = useTranslations('notFound');
  useDocumentTitle(t('title'));
  return (
    <AuthFrame>
      <EmptyState icon="search" title={t('title')} action={<LinkButton href={routes.home}>{t('home')}</LinkButton>}>
        {t('body')}
      </EmptyState>
    </AuthFrame>
  );
}
