'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { Time } from '@/components/ui/Time';
import * as api from '@/lib/api/console';
import type { ServiceAccount, ServiceAccountKey } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import { useServerStatus } from '@/lib/hooks/useServerStatus';
import styles from './ServiceAccountCard.module.css';

/** One service account with its keys. */
export function ServiceAccountCard({
  projectId,
  account,
  editable,
  onDelete,
}: {
  projectId: string;
  account: ServiceAccount;
  editable: boolean;
  onDelete: () => void;
}) {
  const t = useTranslations('serviceAccounts');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const status = useServerStatus();
  const keys = useResource((signal) => api.listServiceAccountKeys(projectId, account.id, signal), [projectId, account.id]);
  const [generating, setGenerating] = useState(false);
  const [removing, setRemoving] = useState<ServiceAccountKey | null>(null);
  const maxKeys = status.data?.limits.maxServiceAccountKeys ?? 10;
  const list = keys.data?.keys ?? [];
  const full = list.length >= maxKeys;

  return (
    <Card
      as="li"
      title={account.displayName || account.email}
      actions={
        editable && (
          <>
            <Button variant="tonal" icon="key" isDisabled={full || !keys.data} onPress={() => setGenerating(true)}>
              {t('generateKey')}
            </Button>
            <IconButton icon="delete" label={t('deleteNamed', { email: account.email })} onPress={onDelete} />
          </>
        )
      }
    >
      <DetailList>
        <Detail label={t('email')} value={account.email} copyLabel={t('copyEmail')} />
        <Detail label={t('uniqueId')} value={account.uniqueId} />
        <Detail label={t('created')}>
          <Time value={account.createTime} />
        </Detail>
      </DetailList>
      <h3 className={styles.keysTitle}>{t('keys')}</h3>
      {keys.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={keys.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(keys.error)}
        </Banner>
      ) : keys.loading && !keys.data ? (
        <Skeleton lines={1} />
      ) : list.length === 0 ? (
        <p className={styles.muted}>{t('noKeys')}</p>
      ) : (
        <Table caption={t('keysOf', { email: account.email })} columns={[t('keyId'), t('created'), t('expires'), ...(editable ? [t('actions')] : [])]}>
          {list.map((key) => (
            <Row key={key.keyId}>
              <Cell>
                <Ltr>{key.keyId}</Ltr>
              </Cell>
              <Cell nowrap>
                <Time value={key.createTime} />
              </Cell>
              <Cell nowrap>{key.validBeforeTime ? <Time value={key.validBeforeTime} /> : t('never')}</Cell>
              {editable && (
                <Cell actions>
                  <IconButton icon="delete" label={t('deleteKey')} onPress={() => setRemoving(key)} />
                </Cell>
              )}
            </Row>
          ))}
        </Table>
      )}
      {editable && full && <p className={styles.muted}>{t('keyLimit', { max: maxKeys })}</p>}
      {generating && (
        <ConfirmDialog
          title={t('generateTitle')}
          confirmLabel={t('generateConfirm')}
          onClose={() => setGenerating(false)}
          onConfirm={async () => {
            const file = await api.generateServiceAccountKey(projectId, account.id);
            keys.reload();
            notify(t('generated', { file }));
          }}
        >
          <Banner tone="warning" title={t('onceTitle')}>
            {t('onceBody')}
          </Banner>
          <p>{t('generateBody')}</p>
        </ConfirmDialog>
      )}
      {removing && (
        <ConfirmDialog
          title={t('deleteKeyTitle')}
          confirmLabel={t('deleteKey')}
          danger
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            await api.deleteServiceAccountKey(projectId, account.id, removing.keyId);
            keys.setData((current) => ({ keys: (current?.keys ?? []).filter((k) => k.keyId !== removing.keyId) }));
            notify(t('keyDeleted'));
          }}
        >
          <p>{t('deleteKeyBody')}</p>
          <p>
            <Ltr>{removing.keyId}</Ltr>
          </p>
        </ConfirmDialog>
      )}
    </Card>
  );
}
