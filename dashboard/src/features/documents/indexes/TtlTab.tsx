'use client';

import { useEffect, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Dialog } from '@/components/ui/Dialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { Menu, MenuItem } from '@/components/ui/Menu';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import * as api from '../api';
import styles from './Indexes.module.css';
import { fieldPathOf, groupOf, hasTtl, idProblem, pathProblem, ttlGroupTaken } from './indexes';
import { POLL_MS } from './ManualTab';

/**
 * Time-to-live policies: one timestamp field per collection group whose documents expire. The
 * Firebase console leaves this to Google Cloud's console, so the wording is that page's. The
 * expiration offset is not offered, since the server deletes at the timestamp itself.
 */
export function TtlTab() {
  const t = useTranslations('documents.indexes.ttl');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const list = useResource((signal) => api.listTtlPolicies(project.projectId, signal), [project.projectId]);
  const [creating, setCreating] = useState(false);
  const [clearing, setClearing] = useState<api.Field | null>(null);
  const policies = (list.data?.fields ?? []).filter(hasTtl);
  const creatingNow = policies.some((f) => f.ttlConfig?.state === 'CREATING');
  const { reload } = list;

  useEffect(() => {
    if (!creatingNow) return;
    const timer = setInterval(reload, POLL_MS);
    return () => clearInterval(timer);
  }, [creatingNow, reload]);

  return (
    <Card
      title={t('title')}
      description={t('description')}
      actions={
        <Button icon="add" isDisabled={!editable} onPress={() => setCreating(true)}>
          {t('create')}
        </Button>
      }
    >
      {list.error && !list.data ? (
        <Banner tone="error" action={<Button variant="text" onPress={list.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(list.error)}
        </Banner>
      ) : !list.data ? (
        <Skeleton lines={3} />
      ) : policies.length === 0 ? (
        <EmptyState icon="history" title={t('empty')} />
      ) : (
        <Table caption={t('caption')} columns={[t('group'), t('field'), t('state'), t('actions')]}>
          {policies.map((f) => (
            <Row key={f.name}>
              <Cell>
                <Ltr>{groupOf(f.name)}</Ltr>
              </Cell>
              <Cell>
                <Ltr>{fieldPathOf(f.name)}</Ltr>
              </Cell>
              <Cell nowrap>
                <Badge tone={f.ttlConfig?.state === 'ACTIVE' ? 'primary' : f.ttlConfig?.state === 'NEEDS_REPAIR' ? 'error' : 'warning'}>
                  {t(`states.${f.ttlConfig?.state ?? 'STATE_UNSPECIFIED'}`)}
                </Badge>
              </Cell>
              <Cell actions>
                <Menu label={t('menu', { group: groupOf(f.name) })} trigger={<IconButton icon="moreVert" label={t('menu', { group: groupOf(f.name) })} />}>
                  <MenuItem icon="delete" danger isDisabled={!editable} onAction={() => setClearing(f)}>
                    {t('delete')}
                  </MenuItem>
                </Menu>
              </Cell>
            </Row>
          ))}
        </Table>
      )}
      {creating && (
        <CreateDialog
          policies={policies}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            notify(t('created'));
            list.reload();
          }}
        />
      )}
      {clearing && (
        <ConfirmDialog
          title={t('deleteTitle')}
          confirmLabel={t('delete')}
          danger
          onClose={() => setClearing(null)}
          onConfirm={async () => {
            await api.clearTtlPolicy(project.projectId, groupOf(clearing.name), fieldPathOf(clearing.name));
            notify(t('deleted'));
            list.reload();
          }}
        >
          <p>{t('deleteBody', { group: groupOf(clearing.name), field: fieldPathOf(clearing.name) })}</p>
        </ConfirmDialog>
      )}
    </Card>
  );
}

function CreateDialog({ policies, onClose, onCreated }: { policies: api.Field[]; onClose: () => void; onCreated: () => void }) {
  const t = useTranslations('documents.indexes.ttl.form');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const [group, setGroup] = useState('');
  const [field, setField] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const groupProblem = idProblem(group) ?? (ttlGroupTaken(policies, group) ? 'taken' : undefined);
  const fieldProblem = pathProblem(field);

  async function submit() {
    setSubmitted(true);
    if (groupProblem || fieldProblem) return;
    setPending(true);
    setError(null);
    try {
      await api.setTtlPolicy(project.projectId, group.trim(), field.trim());
      onCreated();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  return (
    <Dialog
      isOpen
      onOpenChange={(open) => !open && onClose()}
      title={t('title')}
      isBusy={pending}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" isPending={pending}>
            {t('submit')}
          </Button>
        </>
      }
    >
      <div className={styles.rows}>
        <p>{t('perGroup')}</p>
        <p className={styles.muted}>{t('help')}</p>
        <Banner tone="info">{t('indexNotice')}</Banner>
        <TextField
          label={t('group')}
          ltr
          autoFocus
          value={group}
          onChange={setGroup}
          errorMessage={submitted && groupProblem ? t(`problem.${groupProblem}`) : undefined}
        />
        <TextField label={t('field')} ltr value={field} onChange={setField} errorMessage={submitted && fieldProblem ? t('problem.required') : undefined} />
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      </div>
    </Dialog>
  );
}
