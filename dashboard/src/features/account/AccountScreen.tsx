'use client';

import { useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { PageHeader } from '@/components/ui/PageHeader';
import { Select } from '@/components/ui/Select';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Tabs } from '@/components/ui/Tabs';
import { TextField } from '@/components/ui/TextField';
import * as api from '@/lib/api/console';
import type { Account } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useServerStatus } from '@/lib/hooks/useServerStatus';
import { useLocale } from '@/lib/i18n/LocaleProvider';
import { LOCALES, type Locale } from '@/lib/i18n/locales';
import { useSession } from '@/lib/session/SessionProvider';
import { DISPLAY_NAME_MAX } from '@/lib/validation';
import styles from './AccountScreen.module.css';
import { PasswordCard } from './PasswordCard';
import { SessionsCard } from './SessionsCard';
import { TokensCard } from './TokensCard';

/** D-11: the signed-in account: profile and language, security, CLI tokens, server version. */
export function AccountScreen() {
  const t = useTranslations('account');
  const { account } = useSession();
  const [sessionsVersion, setSessionsVersion] = useState(0);
  useDocumentTitle(t('title'));
  if (!account) return null;

  return (
    <>
      <PageHeader title={t('title')} subtitle={account.email} />
      <Tabs
        label={t('sections')}
        items={[
          {
            id: 'profile',
            label: t('profile'),
            content: (
              <>
                <ProfileCard key={account.displayName} account={account} />
                <LanguageCard />
                <ServerCard />
              </>
            ),
          },
          {
            id: 'security',
            label: t('security'),
            content: (
              <>
                <PasswordCard onChanged={() => setSessionsVersion((v) => v + 1)} />
                <SessionsCard key={sessionsVersion} />
              </>
            ),
          },
          { id: 'tokens', label: t('tokens'), content: <TokensCard /> },
        ]}
      />
    </>
  );
}

function ProfileCard({ account }: { account: Account }) {
  const t = useTranslations('account');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { setAccount } = useSession();
  const [name, setName] = useState(account.displayName);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function save(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      const { account: updated } = await api.updateMe({ displayName: name.trim() });
      setAccount(updated);
      notify(t('profileSaved'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  const fieldError = errorText.field(error, 'displayName');
  return (
    <Card title={t('profile')}>
      <DetailList>
        <Detail label={t('email')} value={account.email} />
      </DetailList>
      <form className={styles.form} onSubmit={save} noValidate>
        {Boolean(error) && !fieldError && <Banner tone="error">{errorText.message(error)}</Banner>}
        <TextField label={t('displayName')} value={name} onChange={setName} maxLength={DISPLAY_NAME_MAX} autoComplete="name" errorMessage={fieldError} />
        <div>
          <Button type="submit" isPending={pending} isDisabled={name.trim() === account.displayName}>
            {tCommon('save')}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function LanguageCard() {
  const t = useTranslations('account');
  const tLocale = useTranslations('locale');
  const { locale } = useLocale();
  const { changeLocale } = useSession();
  return (
    <Card title={t('language')} description={t('languageHint')}>
      <Select
        className={styles.language}
        label={t('language')}
        value={locale}
        onChange={(id) => void changeLocale(id as Locale)}
        options={LOCALES.map((l) => ({ id: l, label: tLocale(l) }))}
      />
    </Card>
  );
}

function ServerCard() {
  const t = useTranslations('account');
  const status = useServerStatus();
  return (
    <Card title={t('server')}>
      <DetailList>
        <Detail label={t('version')} value={status.data?.version ?? '…'} />
      </DetailList>
    </Card>
  );
}
