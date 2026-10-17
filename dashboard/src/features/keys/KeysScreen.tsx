'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { CopyButton, Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { Menu, MenuItem } from '@/components/ui/Menu';
import { PageHeader } from '@/components/ui/PageHeader';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { PLATFORMS } from '@/features/apps/platforms';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { ApiKey } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { KeyDialog } from './KeyDialog';
import styles from './KeysScreen.module.css';
import { kindOf, restrictionCount } from './restrictions';

type Action = { kind: 'create' } | { kind: 'edit' | 'rotate' | 'disable'; key: ApiKey };

/** D-08: the project's API keys. They identify the project to the SDKs and are not secrets. */
export function KeysScreen() {
  const t = useTranslations('keys');
  const tCommon = useTranslations('common');
  const tPlatform = useTranslations('platform');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const keys = useResource((signal) => api.listKeys(project.projectId, signal), [project.projectId]);
  const [action, setAction] = useState<Action | null>(null);
  const [enabling, setEnabling] = useState<string | null>(null);
  const [enableError, setEnableError] = useState<unknown>(null);
  const editable = project.state === 'ACTIVE';
  useDocumentTitle(t('title'));

  const replace = (key: ApiKey) =>
    keys.setData((current) => {
      const list = current?.keys ?? [];
      return { keys: list.some((k) => k.uid === key.uid) ? list.map((k) => (k.uid === key.uid ? key : k)) : [...list, key] };
    });

  async function enable(key: ApiKey) {
    setEnabling(key.uid);
    setEnableError(null);
    try {
      replace(await api.setKeyEnabled(project.projectId, key.uid, true));
      notify(t('enabled'));
    } catch (err) {
      setEnableError(err);
    } finally {
      setEnabling(null);
    }
  }

  const list = keys.data?.keys ?? [];
  return (
    <>
      <PageHeader
        title={t('title')}
        subtitle={t('subtitle')}
        actions={
          editable && (
            <Button icon="add" onPress={() => setAction({ kind: 'create' })}>
              {t('create')}
            </Button>
          )
        }
      />
      <Banner tone="info" title={t('notSecretTitle')}>
        {t('notSecretBody')}
      </Banner>
      {Boolean(enableError) && <Banner tone="error">{errorText.message(enableError)}</Banner>}
      {keys.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={keys.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(keys.error)}
        </Banner>
      ) : keys.loading && !keys.data ? (
        <Skeleton lines={4} />
      ) : list.length === 0 ? (
        <EmptyState icon="key" title={t('emptyTitle')}>
          {t('emptyBody')}
        </EmptyState>
      ) : (
        <Table caption={t('title')} columns={[t('name'), t('key'), t('usedBy'), t('restrictions'), t('state'), ...(editable ? [t('actions')] : [])]}>
          {list.map((key) => (
            <Row key={key.uid}>
              <Cell>{key.displayName || t('unnamed')}</Cell>
              <Cell>
                <span className={styles.key}>
                  <Ltr>{key.keyString}</Ltr>
                  <CopyButton value={key.keyString} label={t('copyKey')} />
                </span>
              </Cell>
              <Cell nowrap>{key.platform ? tPlatform(PLATFORMS[key.platform].label) : '—'}</Cell>
              <Cell nowrap>
                {t(`summary.${kindOf(key.restrictions)}`, { count: restrictionCount(key.restrictions) })}
              </Cell>
              <Cell>{key.state === 'ACTIVE' ? <Badge tone="primary">{t('active')}</Badge> : <Badge tone="error">{t('disabled')}</Badge>}</Cell>
              {editable && (
                <Cell actions>
                  <Menu label={t('actionsFor', { name: key.displayName || key.keyString })} trigger={<IconButton icon="moreVert" label={t('actionsFor', { name: key.displayName || key.keyString })} />}>
                    <MenuItem id="edit" icon="edit" onAction={() => setAction({ kind: 'edit', key })}>
                      {t('edit')}
                    </MenuItem>
                    <MenuItem id="rotate" icon="rotate" onAction={() => setAction({ kind: 'rotate', key })}>
                      {t('rotate')}
                    </MenuItem>
                    {key.state === 'ACTIVE' ? (
                      <MenuItem id="disable" icon="block" danger onAction={() => setAction({ kind: 'disable', key })}>
                        {t('disable')}
                      </MenuItem>
                    ) : (
                      <MenuItem id="enable" icon="checkCircle" isDisabled={enabling === key.uid} onAction={() => void enable(key)}>
                        {t('enable')}
                      </MenuItem>
                    )}
                  </Menu>
                </Cell>
              )}
            </Row>
          ))}
        </Table>
      )}
      {action?.kind === 'create' && <KeyDialog projectId={project.projectId} onSaved={replace} onClose={() => setAction(null)} />}
      {action?.kind === 'edit' && <KeyDialog projectId={project.projectId} apiKey={action.key} onSaved={replace} onClose={() => setAction(null)} />}
      {action?.kind === 'rotate' && (
        <ConfirmDialog
          title={t('rotateTitle')}
          confirmLabel={t('rotate')}
          onClose={() => setAction(null)}
          onConfirm={async () => {
            await api.rotateKey(project.projectId, action.key.uid);
            keys.reload();
            notify(t('rotated'));
          }}
        >
          <p>{t('rotateBody')}</p>
          <p>{t('rotateOldKey')}</p>
          <p>
            <Ltr>{action.key.keyString}</Ltr>
          </p>
        </ConfirmDialog>
      )}
      {action?.kind === 'disable' && (
        <ConfirmDialog
          title={t('disableTitle')}
          confirmLabel={t('disable')}
          danger
          onClose={() => setAction(null)}
          onConfirm={async () => {
            replace(await api.setKeyEnabled(project.projectId, action.key.uid, false));
            notify(t('disabledDone'));
          }}
        >
          <p>{t('disableBody')}</p>
          <p>
            <Ltr>{action.key.keyString}</Ltr>
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}
