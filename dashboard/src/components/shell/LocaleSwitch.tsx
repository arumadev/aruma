'use client';

import { useTranslations } from 'use-intl';
import { Button } from '@/components/ui/Button';
import { useLocale } from '@/lib/i18n/LocaleProvider';
import { useSession } from '@/lib/session/SessionProvider';

/**
 * Switches between Arabic and English in place. The button names the other language in that
 * language, which is how people look for it.
 */
export function LocaleSwitch() {
  const t = useTranslations('locale');
  const { locale } = useLocale();
  const { changeLocale } = useSession();
  const other = locale === 'ar' ? 'en' : 'ar';
  return (
    <Button variant="text" icon="language" aria-label={t('switchTo', { language: t(other) })} onPress={() => void changeLocale(other)}>
      <span lang={other}>{t(other)}</span>
    </Button>
  );
}
