'use client';

import { useEffect, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Ltr } from '@/components/ui/Code';
import { Dialog } from '@/components/ui/Dialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { Menu, MenuItem } from '@/components/ui/Menu';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import * as api from '../api';
import styles from './Indexes.module.css';
import { compositeIndex, compositeProblems, groupOf, hasProblems, idOf, KINDS, listedFields, newCompositeForm, SCOPES, kindOf, type CompositeForm, type Kind } from './indexes';
import { Pager, usePager } from './Pager';

/** How often a list is read while an index builds. */
export const POLL_MS = 3000;

/** The composite indexes: what they cover, whether they are built, a form to make one, and delete. */
export function ManualTab() {
  const t = useTranslations('documents.indexes.manual');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const list = useResource((signal) => api.listIndexes(project.projectId, signal), [project.projectId]);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<api.Index | null>(null);
  const indexes = list.data?.indexes ?? [];
  const pager = usePager(indexes, 10);
  const building = indexes.some((i) => i.state === 'CREATING');
  const { reload } = list;

  // A building index becomes Enabled on its own: the list is read again until none builds.
  useEffect(() => {
    if (!building) return;
    const timer = setInterval(reload, POLL_MS);
    return () => clearInterval(timer);
  }, [building, reload]);

  const create = (
    <Button icon="add" isDisabled={!editable} onPress={() => setCreating(true)}>
      {t('create')}
    </Button>
  );

  return (
    <div className={styles.stack}>
      {list.error && !list.data ? (
        <Banner tone="error" action={<Button variant="text" onPress={list.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(list.error)}
        </Banner>
      ) : !list.data ? (
        <Skeleton lines={4} />
      ) : indexes.length === 0 ? (
        <EmptyState icon="layers" title={t('emptyTitle')} action={create}>
          {t('emptyBody')}
        </EmptyState>
      ) : (
        <>
          <div className={styles.toolbar}>
            <p className={styles.muted}>{t('description')}</p>
            {create}
          </div>
          <Table caption={t('caption')} columns={[t('collection'), t('fields'), t('scope'), t('id'), t('status'), t('actions')]}>
            {pager.rows.map((ix) => {
              const name = ix.name ?? '';
              return (
                <Row key={name || JSON.stringify(ix.fields)}>
                  <Cell>
                    <Ltr>{groupOf(name)}</Ltr>
                  </Cell>
                  <Cell>
                    <IndexFields index={ix} />
                  </Cell>
                  <Cell nowrap>{t(`scopes.${ix.queryScope}`)}</Cell>
                  <Cell>
                    <Ltr>{idOf(name)}</Ltr>
                  </Cell>
                  <Cell nowrap>
                    <StateBadge state={ix.state} />
                  </Cell>
                  <Cell actions>
                    <Menu label={t('menu', { id: idOf(name) })} trigger={<IconButton icon="moreVert" label={t('menu', { id: idOf(name) })} />}>
                      <MenuItem icon="delete" danger isDisabled={!editable || ix.state === 'CREATING'} onAction={() => setDeleting(ix)}>
                        {t('delete')}
                      </MenuItem>
                    </Menu>
                  </Cell>
                </Row>
              );
            })}
          </Table>
          <Pager pager={pager} />
        </>
      )}
      {creating && (
        <CreateDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            notify(t('created'));
            list.reload();
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={t('deleteTitle')}
          confirmLabel={t('delete')}
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await api.deleteIndex(project.projectId, deleting.name ?? '');
            notify(t('deleted'));
            list.reload();
          }}
        >
          <p>{t('deleteBody')}</p>
          <dl className={styles.summary}>
            <div>
              <dt>{t('collection')}</dt>
              <dd>
                <Ltr>{groupOf(deleting.name ?? '')}</Ltr>
              </dd>
            </div>
            <div>
              <dt>{t('fields')}</dt>
              <dd>
                <IndexFields index={deleting} />
              </dd>
            </div>
          </dl>
        </ConfirmDialog>
      )}
    </div>
  );
}

function StateBadge({ state }: { state: api.Index['state'] }) {
  const t = useTranslations('documents.indexes.manual.state');
  if (state === 'CREATING') return <Badge tone="warning">{t('creating')}</Badge>;
  if (state === 'NEEDS_REPAIR') return <Badge tone="error">{t('needsRepair')}</Badge>;
  return <Badge tone="primary">{t('ready')}</Badge>;
}

/** An index's fields, each with its direction; `__name__` last, as the console lists it. */
export function IndexFields({ index }: { index: api.Index }) {
  const t = useTranslations('documents.indexes.kind');
  return (
    <ul className={styles.fields}>
      {listedFields(index).map((f, i) => (
        <li key={i}>
          <Ltr>{f.fieldPath ?? ''}</Ltr> <span className={styles.muted}>{t(kindOf(f))}</span>
        </li>
      ))}
    </ul>
  );
}

function CreateDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const t = useTranslations('documents.indexes.manual.form');
  const tKind = useTranslations('documents.indexes.kind');
  const tManual = useTranslations('documents.indexes.manual');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const [form, setForm] = useState<CompositeForm>(newCompositeForm);
  const [submitted, setSubmitted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const problems = compositeProblems(form);
  const show = submitted ? problems : null;

  async function submit() {
    setSubmitted(true);
    if (hasProblems(problems)) return;
    setPending(true);
    setError(null);
    try {
      await api.createIndex(project.projectId, form.collection.trim(), compositeIndex(form));
      onCreated();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  const setField = (i: number, patch: Partial<CompositeForm['fields'][number]>) =>
    setForm({ ...form, fields: form.fields.map((f, n) => (n === i ? { ...f, ...patch } : f)) });

  return (
    <Dialog
      isOpen
      wide
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
        <TextField
          label={t('collection')}
          ltr
          autoFocus
          placeholder={t('collectionPlaceholder')}
          value={form.collection}
          onChange={(v) => setForm({ ...form, collection: v })}
          errorMessage={show?.collection && t(`problem.${show.collection}`)}
        />
        <fieldset className={styles.group}>
          <legend className={styles.legend}>{t('fields')}</legend>
          {form.fields.map((f, i) => (
            <div key={i} className={styles.fieldRow}>
              <TextField
                label={t('fieldPath', { n: i + 1 })}
                ltr
                value={f.path}
                onChange={(v) => setField(i, { path: v })}
                errorMessage={show?.fields[i] && t('problem.required')}
              />
              <Select
                label={t('order', { n: i + 1 })}
                value={f.kind}
                onChange={(id) => setField(i, { kind: id as Kind })}
                options={KINDS.map((k) => ({ id: k, label: tKind(k) }))}
              />
              <IconButton
                icon="close"
                label={t('removeField', { n: i + 1 })}
                isDisabled={form.fields.length <= 2}
                onPress={() => setForm({ ...form, fields: form.fields.filter((_, n) => n !== i) })}
              />
            </div>
          ))}
          <div>
            <Button variant="text" icon="add" onPress={() => setForm({ ...form, fields: [...form.fields, { path: '', kind: 'ASCENDING' }] })}>
              {t('addField')}
            </Button>
          </div>
          <p className={styles.muted}>{t('nameNote')}</p>
        </fieldset>
        <Select
          label={t('scope')}
          value={form.scope}
          onChange={(id) => setForm({ ...form, scope: id as CompositeForm['scope'] })}
          options={SCOPES.map((s) => ({ id: s, label: tManual(`scopes.${s}`) }))}
        />
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      </div>
    </Dialog>
  );
}
