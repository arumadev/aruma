'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { EmptyState } from '@/components/ui/EmptyState';
import { PageHeader } from '@/components/ui/PageHeader';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { Cell, Row, Table } from '@/components/ui/Table';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { AuditEntry } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import styles from './AuditScreen.module.css';

const PAGE_SIZE = 50;
const ALL = 'all';

// Every action the server records (internal/tenancy/core).
export const AUDIT_ACTIONS = [
  'project.create',
  'project.update',
  'project.delete',
  'project.restore',
  'app.create',
  'app.update',
  'app.delete',
  'app.undelete',
  'app.sha.add',
  'app.sha.delete',
  'apikey.create',
  'apikey.update',
  'apikey.rotate',
  'apikey.disable',
  'apikey.enable',
  'serviceaccount.create',
  'serviceaccount.delete',
  'serviceaccount.key.create',
  'serviceaccount.key.delete',
] as const;

type AuditAction = (typeof AUDIT_ACTIONS)[number];
const isAuditAction = (code: string): code is AuditAction => (AUDIT_ACTIONS as readonly string[]).includes(code);

/** D-12: who changed what in the project, newest first, filterable by action. */
export function AuditScreen() {
  const t = useTranslations('audit');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const log = useResource((signal) => api.listAudit(project.projectId, { pageSize: PAGE_SIZE }, signal), [project.projectId]);
  const [action, setAction] = useState<string>(ALL);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<unknown>(null);
  useDocumentTitle(t('title'));

  const actionLabel = (code: string) => (isAuditAction(code) ? t(`actions.${code}`) : code);

  async function loadMore() {
    const token = log.data?.nextPageToken;
    if (!token) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await api.listAudit(project.projectId, { pageSize: PAGE_SIZE, pageToken: token });
      log.setData((current) => ({ entries: [...(current?.entries ?? []), ...page.entries], nextPageToken: page.nextPageToken }));
    } catch (err) {
      setMoreError(err);
    } finally {
      setLoadingMore(false);
    }
  }

  const entries = log.data?.entries ?? [];
  const visible = action === ALL ? entries : entries.filter((e) => e.action === action);
  const known = new Set<string>(AUDIT_ACTIONS);
  const extra = [...new Set(entries.map((e) => e.action))].filter((a) => !known.has(a));

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <div className={styles.toolbar}>
        <Select
          className={styles.filter}
          label={t('filter')}
          value={action}
          onChange={setAction}
          options={[{ id: ALL, label: t('allActions') }, ...[...AUDIT_ACTIONS, ...extra].map((a) => ({ id: a, label: actionLabel(a), detail: a }))]}
        />
      </div>
      {log.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={log.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(log.error)}
        </Banner>
      ) : log.loading && !log.data ? (
        <Skeleton lines={6} />
      ) : visible.length === 0 ? (
        <EmptyState icon="history" title={entries.length === 0 ? t('emptyTitle') : t('noMatchTitle')}>
          {entries.length === 0 ? t('emptyBody') : log.data?.nextPageToken ? t('noMatchMore') : t('noMatchBody')}
        </EmptyState>
      ) : (
        <Table caption={t('title')} columns={[t('time'), t('action'), t('target'), t('actor'), t('result'), t('ip')]}>
          {visible.map((entry) => (
            <AuditRow key={entry.id} entry={entry} label={actionLabel(entry.action)} />
          ))}
        </Table>
      )}
      {log.data?.nextPageToken && (
        <div className={styles.more}>
          {Boolean(moreError) && <Banner tone="error">{errorText.message(moreError)}</Banner>}
          <Button variant="outlined" isPending={loadingMore} onPress={loadMore}>
            {t('loadOlder')}
          </Button>
        </div>
      )}
    </>
  );
}

function AuditRow({ entry, label }: { entry: AuditEntry; label: string }) {
  const t = useTranslations('audit');
  return (
    <Row>
      <Cell nowrap>
        <Time value={entry.time} />
      </Cell>
      <Cell>
        <span className={styles.action}>
          <span>{label}</span>
          <Ltr>{entry.action}</Ltr>
        </span>
      </Cell>
      <Cell>{entry.target ? <Ltr>{entry.target}</Ltr> : '—'}</Cell>
      <Cell>
        <span className={styles.action}>
          <span>{entry.actorType === 'service_account' ? t('actorServiceAccount') : t('actorAccount')}</span>
          {entry.actorId && <Ltr>{entry.actorId}</Ltr>}
        </span>
      </Cell>
      <Cell nowrap>{entry.result === 'ok' ? t('resultOk') : <Ltr>{entry.result}</Ltr>}</Cell>
      <Cell nowrap>{entry.ip ? <Ltr>{entry.ip}</Ltr> : '—'}</Cell>
    </Row>
  );
}
