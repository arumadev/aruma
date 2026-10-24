'use client';

import { useFormatter, useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { Skeleton } from '@/components/ui/Skeleton';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import * as api from './api';
import { useBytes } from './Bytes';
import { useStorageSection } from './StorageSection';
import styles from './UsageScreen.module.css';

/**
 * What the project's files take against its quota: the stored bytes, and those that uploads in
 * progress have reserved, which count too until they finish or expire.
 */
export function UsageScreen() {
  const t = useTranslations('storage.usage');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const format = useFormatter();
  const bytes = useBytes();
  const { project } = useProject();
  const { limits } = useStorageSection();
  const usage = useResource((signal) => api.getUsage(project.projectId, signal), [project.projectId]);
  useDocumentTitle(t('title'));

  if (usage.error && !usage.data) {
    return (
      <Banner tone="error" action={<Button variant="text" onPress={usage.reload}>{tCommon('retry')}</Button>}>
        {errorText.message(usage.error)}
      </Banner>
    );
  }
  const u = usage.data;
  if (!u) return <Skeleton lines={4} />;
  const taken = u.usedBytes + u.reservedBytes;
  const share = u.quotaBytes > 0 ? Math.min(taken / u.quotaBytes, 1) : 0;

  return (
    <>
      <Card
        title={t('title')}
        description={t('description')}
        actions={
          <Button variant="outlined" icon="rotate" isPending={usage.loading} onPress={usage.reload}>
            {t('refresh')}
          </Button>
        }
      >
        {u.quotaBytes > 0 ? (
          <div className={styles.quota}>
            <p className={styles.summary}>
              {t('ofQuota', { taken: bytes(taken), quota: bytes(u.quotaBytes), share: format.number(share, { style: 'percent', maximumFractionDigits: 1 }) })}
            </p>
            <meter className={styles.meter} min={0} max={u.quotaBytes} low={u.quotaBytes * 0.8} high={u.quotaBytes * 0.95} optimum={0} value={taken} aria-label={t('meter')} />
          </div>
        ) : (
          <p className={styles.summary}>{t('unlimited', { taken: bytes(taken) })}</p>
        )}
        {share >= 0.95 && <Banner tone="warning">{t('nearlyFull')}</Banner>}
        <DetailList>
          <Detail label={t('used')}>{bytes(u.usedBytes)}</Detail>
          <Detail label={t('reserved')}>{bytes(u.reservedBytes)}</Detail>
          <Detail label={t('objects')}>{format.number(u.objects)}</Detail>
          <Detail label={t('quota')}>{u.quotaBytes > 0 ? bytes(u.quotaBytes) : t('noQuota')}</Detail>
        </DetailList>
      </Card>
      {limits && (
        <Card title={t('limits')} description={t('limitsDescription')}>
          <DetailList>
            <Detail label={t('objectBytes')}>{bytes(limits.objectBytes)}</Detail>
            <Detail label={t('metadataBytes')}>{bytes(limits.metadataBytes)}</Detail>
            <Detail label={t('sessionTime')}>{t('minutes', { count: Math.round(limits.uploadSessionSeconds / 60) })}</Detail>
          </DetailList>
        </Card>
      )}
    </>
  );
}
