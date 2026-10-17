'use client';

import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { PageHeader } from '@/components/ui/PageHeader';
import { Skeleton } from '@/components/ui/Skeleton';
import { Cell, Row, Table } from '@/components/ui/Table';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useServerStatus } from '@/lib/hooks/useServerStatus';
import styles from './LimitsScreen.module.css';

/** The published limits of this release, read from the server. */
export function LimitsScreen() {
  const t = useTranslations('limits');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const status = useServerStatus();
  useDocumentTitle(t('title'));

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      {status.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={status.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(status.error)}
        </Banner>
      ) : !status.data ? (
        <Skeleton lines={4} />
      ) : (
        <>
          <Table caption={t('title')} columns={[t('limit'), t('value')]}>
            <Row>
              <Cell>{t('servers')}</Cell>
              <Cell nowrap>{t('serversValue', { count: status.data.limits.servers })}</Cell>
            </Row>
            <Row>
              <Cell>{t('serviceAccountKeys')}</Cell>
              <Cell nowrap>{t('keysValue', { count: status.data.limits.maxServiceAccountKeys })}</Cell>
            </Row>
            <Row>
              <Cell>{t('retention')}</Cell>
              <Cell nowrap>{t('daysValue', { count: status.data.limits.deletedRetentionDays })}</Cell>
            </Row>
          </Table>
          <Card title={t('notesTitle')}>
            <ul className={styles.notes}>
              <li>{t('noteSingle')}</li>
              <li>{t('noteRetention', { days: status.data.limits.deletedRetentionDays })}</li>
              <li>{t('noteKeys', { max: status.data.limits.maxServiceAccountKeys })}</li>
            </ul>
            <p className={styles.version}>
              {t('version')} <Ltr>{status.data.version}</Ltr>
            </p>
          </Card>
        </>
      )}
    </>
  );
}
