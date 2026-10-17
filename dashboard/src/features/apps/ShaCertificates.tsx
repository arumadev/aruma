'use client';

import { useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { ApiError } from '@/lib/api/client';
import * as api from '@/lib/api/console';
import type { ShaCertificate } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import { formatSha, normalizeSha } from '@/lib/validation';
import styles from './ShaCertificates.module.css';

const KEYTOOL = 'keytool -list -v -alias androiddebugkey -keystore ~/.android/debug.keystore';

/** The signing-certificate fingerprints of an Android app: list, add, delete. */
export function ShaCertificates({ projectId, appId, editable }: { projectId: string; appId: string; editable: boolean }) {
  const t = useTranslations('sha');
  const tCommon = useTranslations('common');
  const tErrors = useTranslations('errors');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const list = useResource((signal) => api.listSha(projectId, appId, signal), [projectId, appId]);
  const [input, setInput] = useState('');
  const [attempted, setAttempted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [removing, setRemoving] = useState<ShaCertificate | null>(null);

  const hex = normalizeSha(input);
  const duplicate = error instanceof ApiError && error.status === 'ALREADY_EXISTS';
  const fieldError =
    attempted && !hex ? tErrors('field.shaHash') : duplicate ? t('duplicate') : errorText.field(error, 'shaHash');

  async function add(event: FormEvent) {
    event.preventDefault();
    setAttempted(true);
    if (!hex) return;
    setPending(true);
    setError(null);
    try {
      const cert = await api.addSha(projectId, appId, hex);
      list.setData((current) => ({ certificates: [...(current?.certificates ?? []), cert] }));
      setInput('');
      setAttempted(false);
      notify(t('added'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  const certificates = list.data?.certificates ?? [];
  return (
    <Card title={t('title')} description={t('description')}>
      {list.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={list.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(list.error)}
        </Banner>
      ) : list.loading && !list.data ? (
        <Skeleton lines={2} />
      ) : certificates.length === 0 ? (
        <p className={styles.empty}>{t('empty')}</p>
      ) : (
        <Table caption={t('title')} columns={[t('type'), t('fingerprint'), ...(editable ? [t('actions')] : [])]}>
          {certificates.map((cert) => (
            <Row key={cert.id}>
              <Cell nowrap>{cert.certType === 'SHA_1' ? 'SHA-1' : 'SHA-256'}</Cell>
              <Cell>
                <Ltr>{formatSha(cert.shaHash)}</Ltr>
              </Cell>
              {editable && (
                <Cell actions>
                  <IconButton icon="delete" label={t('remove')} onPress={() => setRemoving(cert)} />
                </Cell>
              )}
            </Row>
          ))}
        </Table>
      )}
      {editable && (
        <form className={styles.add} onSubmit={add} noValidate>
          <TextField
            className={styles.field}
            label={t('add')}
            value={input}
            onChange={(value) => {
              setInput(value);
              setError(null);
            }}
            ltr
            placeholder="AB:CD:EF:…"
            description={t('hint')}
            errorMessage={fieldError}
          />
          <Button type="submit" variant="tonal" icon="add" isPending={pending} className={styles.button}>
            {t('addButton')}
          </Button>
        </form>
      )}
      {editable && (
        <details className={styles.help}>
          <summary>{t('howTo')}</summary>
          <pre dir="ltr" className={styles.command}>
            <code>{KEYTOOL}</code>
          </pre>
        </details>
      )}
      {removing && (
        <ConfirmDialog
          title={t('removeTitle')}
          confirmLabel={t('remove')}
          danger
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            await api.deleteSha(projectId, appId, removing.id);
            list.setData((current) => ({ certificates: (current?.certificates ?? []).filter((c) => c.id !== removing.id) }));
            notify(t('removed'));
          }}
        >
          <p>{t('removeBody')}</p>
          <p>
            <Ltr>{formatSha(removing.shaHash)}</Ltr>
          </p>
        </ConfirmDialog>
      )}
    </Card>
  );
}
