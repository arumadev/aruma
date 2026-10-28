'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { Card } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Dialog } from '@/components/ui/Dialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { Menu, MenuItem } from '@/components/ui/Menu';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Switch } from '@/components/ui/Switch';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import * as api from '../api';
import styles from './Indexes.module.css';
import {
  automaticSettings,
  countOn,
  enabledKinds,
  exemptionForm,
  exemptionIndexes,
  fieldPathOf,
  groupOf,
  idProblem,
  isExemption,
  KINDS,
  newExemptionForm,
  pathProblem,
  type ExemptionForm,
  type KindSet,
} from './indexes';
import { Pager, usePager } from './Pager';
import { useOperationWatch } from './useOperationWatch';

/** The automatic single-field indexes, what the database does by default, and the fields that differ. */
export function AutomaticTab() {
  const t = useTranslations('documents.indexes.automatic');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const list = useResource((signal) => api.listExemptions(project.projectId, signal), [project.projectId]);
  const [editing, setEditing] = useState<ExemptionForm | 'new' | null>(null);
  const [clearing, setClearing] = useState<api.Field | null>(null);
  const fields = list.data?.fields ?? [];
  const exemptions = fields.filter(isExemption);
  const pager = usePager(exemptions, 25);
  const settings = automaticSettings(fields);
  const watch = useOperationWatch(project.projectId, list.reload);

  return (
    <div className={styles.stack}>
      <Card title={t('settingsTitle')} description={t('settingsBody')}>
        <p>
          {t('summary', { collection: countOn(settings.COLLECTION), group: countOn(settings.COLLECTION_GROUP) })}
        </p>
        <Table caption={t('settingsCaption')} columns={[t('type'), t('collectionScope'), t('groupScope')]}>
          {KINDS.map((k) => (
            <Row key={k}>
              <Cell>{t(`kind.${k}`)}</Cell>
              <Cell>{settings.COLLECTION[k] ? t('enabled') : t('disabled')}</Cell>
              <Cell>{settings.COLLECTION_GROUP[k] ? t('enabled') : t('disabled')}</Cell>
            </Row>
          ))}
        </Table>
      </Card>
      <Card
        title={t('exemptionsTitle')}
        description={t('exemptionsBody')}
        actions={
          <Button icon="add" isDisabled={!editable} onPress={() => setEditing('new')}>
            {t('addExemption')}
          </Button>
        }
      >
        {list.error && !list.data ? (
          <Banner tone="error" action={<Button variant="text" onPress={list.reload}>{tCommon('retry')}</Button>}>
            {errorText.message(list.error)}
          </Banner>
        ) : !list.data ? (
          <Skeleton lines={3} />
        ) : exemptions.length === 0 ? (
          <EmptyState icon="layers" title={t('noExemptions')}>
            {t('noExemptionsBody')}
          </EmptyState>
        ) : (
          <>
            <Table caption={t('exemptionsCaption')} columns={[t('collection'), t('fieldPath'), t('collectionScope'), t('groupScope'), t('actions')]}>
              {pager.rows.map((f) => (
                <Row key={f.name}>
                  <Cell>
                    <Ltr>{groupOf(f.name)}</Ltr>
                  </Cell>
                  <Cell>
                    <Ltr>{fieldPathOf(f.name)}</Ltr>
                  </Cell>
                  <Cell>
                    <Chips on={enabledKinds(f, 'COLLECTION')} />
                  </Cell>
                  <Cell>
                    {countOn(enabledKinds(f, 'COLLECTION_GROUP')) > 0 ? <Chips on={enabledKinds(f, 'COLLECTION_GROUP')} /> : <span className={styles.muted}>{t('noChanges')}</span>}
                  </Cell>
                  <Cell actions>
                    <Menu label={t('menu', { field: fieldPathOf(f.name) })} trigger={<IconButton icon="moreVert" label={t('menu', { field: fieldPathOf(f.name) })} />}>
                      <MenuItem icon="edit" isDisabled={!editable} onAction={() => setEditing(exemptionForm(f))}>
                        {t('edit')}
                      </MenuItem>
                      <MenuItem icon="delete" danger isDisabled={!editable} onAction={() => setClearing(f)}>
                        {t('delete')}
                      </MenuItem>
                    </Menu>
                  </Cell>
                </Row>
              ))}
            </Table>
            <Pager pager={pager} />
          </>
        )}
      </Card>
      {editing && (
        <ExemptionDialog
          defaults={settings}
          initial={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(op) => {
            watch(op);
            setEditing(null);
            notify(t('saved'));
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
            watch(await api.clearExemption(project.projectId, groupOf(clearing.name), fieldPathOf(clearing.name)));
            notify(t('deleted'));
            list.reload();
          }}
        >
          <p>{t('deleteBody')}</p>
          <dl className={styles.summary}>
            <div>
              <dt>{t('collection')}</dt>
              <dd>
                <Ltr>{groupOf(clearing.name)}</Ltr>
              </dd>
            </div>
            <div>
              <dt>{t('fieldPath')}</dt>
              <dd>
                <Ltr>{fieldPathOf(clearing.name)}</Ltr>
              </dd>
            </div>
          </dl>
          <p className={styles.muted}>{t('deleteWarning')}</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

function Chips({ on }: { on: KindSet }) {
  const t = useTranslations('documents.indexes.automatic');
  return (
    <span className={styles.chips}>
      {KINDS.map((k) => (
        <span key={k} className={on[k] ? undefined : styles.off}>
          <Badge tone={on[k] ? 'primary' : 'neutral'}>{on[k] ? t(`kind.${k}`) : t('chipOff', { kind: t(`kind.${k}`) })}</Badge>
        </span>
      ))}
    </span>
  );
}

/** The two steps of an exemption: the field and its scopes, then the three toggles. */
function ExemptionDialog({
  initial,
  defaults,
  onClose,
  onSaved,
}: {
  initial: ExemptionForm | null;
  defaults: Record<api.QueryScope, KindSet>;
  onClose: () => void;
  onSaved: (op: api.Operation) => void;
}) {
  const t = useTranslations('documents.indexes.automatic.form');
  const tAuto = useTranslations('documents.indexes.automatic');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const [form, setForm] = useState<ExemptionForm>(initial ?? newExemptionForm());
  const [step, setStep] = useState<1 | 2>(initial ? 2 : 1);
  const [submitted, setSubmitted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const collectionProblem = idProblem(form.collection);
  const fieldProblem = pathProblem(form.field);
  const noScope = !form.scopes.COLLECTION && !form.scopes.COLLECTION_GROUP;

  async function save() {
    setPending(true);
    setError(null);
    try {
      onSaved(await api.setExemption(project.projectId, form.collection.trim(), form.field.trim(), { indexes: exemptionIndexes(form, defaults) }));
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  function submit() {
    if (step === 1) {
      setSubmitted(true);
      if (collectionProblem || fieldProblem || noScope) return;
      setStep(2);
      return;
    }
    void save();
  }

  const scopeNames = [form.scopes.COLLECTION && tAuto('scopeName.COLLECTION'), form.scopes.COLLECTION_GROUP && tAuto('scopeName.COLLECTION_GROUP')].filter(Boolean).join(', ');

  return (
    <Dialog
      isOpen
      wide
      onOpenChange={(open) => !open && onClose()}
      title={step === 1 ? t('step1') : t('step2')}
      isBusy={pending}
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {tCommon('cancel')}
          </Button>
          {step === 2 && !initial && (
            <Button variant="outlined" onPress={() => setStep(1)} isDisabled={pending}>
              {t('back')}
            </Button>
          )}
          <Button type="submit" isPending={pending}>
            {step === 1 ? t('next') : tCommon('save')}
          </Button>
        </>
      }
    >
      {step === 1 ? (
        <div className={styles.rows}>
          <TextField
            label={t('collection')}
            ltr
            autoFocus
            placeholder={t('collectionPlaceholder')}
            value={form.collection}
            onChange={(v) => setForm({ ...form, collection: v })}
            errorMessage={submitted && collectionProblem ? t(`problem.${collectionProblem}`) : undefined}
          />
          <TextField
            label={t('field')}
            ltr
            placeholder={t('fieldPlaceholder')}
            value={form.field}
            onChange={(v) => setForm({ ...form, field: v })}
            errorMessage={submitted && fieldProblem ? t('problem.required') : undefined}
          />
          <fieldset className={styles.group}>
            <legend className={styles.legend}>{t('scopes')}</legend>
            {(['COLLECTION', 'COLLECTION_GROUP'] as const).map((s) => (
              <label key={s} className={styles.check}>
                <input type="checkbox" checked={form.scopes[s]} onChange={(e) => setForm({ ...form, scopes: { ...form.scopes, [s]: e.target.checked } })} />
                {tAuto(`scopeName.${s}`)}
              </label>
            ))}
            {submitted && noScope && <p role="alert" className={styles.muted}>{t('problem.scope')}</p>}
          </fieldset>
        </div>
      ) : (
        <div className={styles.rows}>
          <dl className={styles.summary}>
            <div>
              <dt>{t('collection')}</dt>
              <dd>
                <Ltr>{form.collection.trim()}</Ltr>
              </dd>
            </div>
            <div>
              <dt>{t('field')}</dt>
              <dd>
                <Ltr>{form.field.trim()}</Ltr>
              </dd>
            </div>
            <div>
              <dt>{t('scopes')}</dt>
              <dd>{scopeNames}</dd>
            </div>
          </dl>
          {KINDS.map((k) => (
            <Switch key={k} isSelected={form.kinds[k]} onChange={(v) => setForm({ ...form, kinds: { ...form.kinds, [k]: v } })}>
              {tAuto(`kind.${k}`)}
            </Switch>
          ))}
          <Banner tone="warning">{t('warning')}</Banner>
        </div>
      )}
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
    </Dialog>
  );
}
