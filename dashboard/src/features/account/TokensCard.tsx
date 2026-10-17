'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { CodeBlock, Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Dialog } from '@/components/ui/Dialog';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { Time } from '@/components/ui/Time';
import * as api from '@/lib/api/console';
import type { Token } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import styles from './AccountScreen.module.css';

const LIFETIMES = ['30', '90', '366', '0'] as const;

/** Personal CLI tokens: the plaintext is shown once, right after creation. */
export function TokensCard() {
  const t = useTranslations('tokens');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const tokens = useResource((signal) => api.listTokens(signal), []);
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<Token | null>(null);

  const list = tokens.data?.tokens ?? [];
  return (
    <Card
      title={t('title')}
      description={t('description')}
      actions={
        <Button icon="add" onPress={() => setCreating(true)}>
          {t('create')}
        </Button>
      }
    >
      {tokens.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={tokens.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(tokens.error)}
        </Banner>
      ) : tokens.loading && !tokens.data ? (
        <Skeleton lines={2} />
      ) : list.length === 0 ? (
        <p className={styles.muted}>{t('empty')}</p>
      ) : (
        <Table caption={t('title')} columns={[t('name'), t('prefix'), t('created'), t('lastUsed'), t('expires'), t('actions')]}>
          {list.map((token) => (
            <Row key={token.id}>
              <Cell>
                <bdi>{token.name}</bdi>
              </Cell>
              <Cell nowrap>
                <Ltr>{`${token.prefix}…`}</Ltr>
              </Cell>
              <Cell nowrap>
                <Time value={token.createTime} />
              </Cell>
              <Cell nowrap>{token.lastUsedTime ? <Time value={token.lastUsedTime} /> : t('never')}</Cell>
              <Cell nowrap>{token.expireTime ? <Time value={token.expireTime} /> : t('noExpiry')}</Cell>
              <Cell actions>
                <Button variant="text" onPress={() => setRevoking(token)}>
                  {t('revoke')}
                </Button>
              </Cell>
            </Row>
          ))}
        </Table>
      )}
      {creating && (
        <CreateTokenDialog
          onCreated={(token) => tokens.setData((current) => ({ tokens: [...(current?.tokens ?? []), { ...token, token: undefined }] }))}
          onClose={() => setCreating(false)}
        />
      )}
      {revoking && (
        <ConfirmDialog
          title={t('revokeTitle')}
          confirmLabel={t('revoke')}
          danger
          onClose={() => setRevoking(null)}
          onConfirm={async () => {
            await api.revokeToken(revoking.id);
            tokens.setData((current) => ({ tokens: (current?.tokens ?? []).filter((x) => x.id !== revoking.id) }));
            notify(t('revoked'));
          }}
        >
          <p>{t('revokeBody', { name: revoking.name })}</p>
        </ConfirmDialog>
      )}
    </Card>
  );
}

function CreateTokenDialog({ onCreated, onClose }: { onCreated: (token: Token) => void; onClose: () => void }) {
  const t = useTranslations('tokens');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const [name, setName] = useState('');
  const [lifetime, setLifetime] = useState<string>('90');
  const [attempted, setAttempted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [plaintext, setPlaintext] = useState<string | null>(null);

  async function create() {
    setAttempted(true);
    if (!name.trim()) return;
    setPending(true);
    setError(null);
    try {
      const token = await api.createToken({ name: name.trim(), expireDays: Number(lifetime) });
      onCreated(token);
      setPlaintext(token.token ?? '');
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  if (plaintext !== null) {
    return (
      <Dialog
        isOpen
        role="alertdialog"
        onOpenChange={(open) => !open && onClose()}
        title={t('createdTitle')}
        actions={<Button onPress={onClose}>{t('done')}</Button>}
      >
        <Banner tone="warning" title={t('onceTitle')}>
          {t('onceBody')}
        </Banner>
        <CodeBlock label={t('token')} code={plaintext} copyLabel={t('copy')} />
        <p>{t('usage')}</p>
        <CodeBlock label={t('usageLabel')} code={`Authorization: Bearer ${plaintext}`} copyLabel={t('copyUsage')} />
      </Dialog>
    );
  }

  const fieldError = attempted && !name.trim() ? t('nameRequired') : errorText.field(error, 'name');
  return (
    <Dialog
      isOpen
      onOpenChange={(open) => !open && onClose()}
      title={t('createTitle')}
      isBusy={pending}
      onSubmit={create}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" isPending={pending}>
            {t('create')}
          </Button>
        </>
      }
    >
      {Boolean(error) && !fieldError && <Banner tone="error">{errorText.message(error)}</Banner>}
      <TextField label={t('name')} value={name} onChange={setName} maxLength={100} isRequired autoFocus description={t('nameHint')} errorMessage={fieldError} />
      <Select
        label={t('lifetime')}
        value={lifetime}
        onChange={setLifetime}
        options={LIFETIMES.map((days) => ({ id: days, label: days === '0' ? t('noExpiry') : t('days', { days: Number(days) }) }))}
      />
    </Dialog>
  );
}
