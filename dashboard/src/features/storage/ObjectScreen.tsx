'use client';

import { useRouter, useSearchParams } from 'next/navigation';
import { useRef, useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton, LinkButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { CopyButton, Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import * as api from './api';
import { useBytes } from './Bytes';
import { formOf, hasProblems, patchOf, problemsOf, type MetadataForm } from './metadata';
import styles from './ObjectScreen.module.css';
import { isRasterImage, leafName, parentPrefix } from './paths';
import { useStorageSection } from './StorageSection';

// Defaults the form checks against before the server's limits load: the server's own defaults.
const FALLBACK_LIMITS = { headerFieldBytes: 1024, metadataBytes: 8192 };

/** One file: what it is, a preview for images, its metadata, and the links that download it. */
export function ObjectScreen() {
  const t = useTranslations('storage.object');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const bytes = useBytes();
  const router = useRouter();
  const { project } = useProject();
  const { bucket } = useStorageSection();
  const projectId = project.projectId;
  const editable = project.state === 'ACTIVE';
  const name = useSearchParams().get('name') ?? '';
  const object = useResource((signal) => api.getObject(projectId, name, signal), [projectId, name], name !== '');
  const [deleting, setDeleting] = useState(false);
  const folder = parentPrefix(name);
  useDocumentTitle(leafName(name, folder) || t('title'));

  const back = (
    <div>
      <LinkButton variant="text" icon="arrowBack" href={routes.storage(projectId, folder)}>
        {folder ? t('backTo', { folder }) : t('back')}
      </LinkButton>
    </div>
  );
  if (object.error && !object.data) {
    return (
      <>
        {back}
        <Banner tone="error" action={<Button variant="text" onPress={object.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(object.error)}
        </Banner>
      </>
    );
  }
  const o = object.data;
  if (!o) return <Skeleton lines={3} blocks={2} />;

  return (
    <>
      {back}
      <div className={styles.heading}>
        <h2 className={styles.name}>
          <Ltr>{leafName(o.name, folder)}</Ltr>
        </h2>
        <div className={styles.buttons}>
          <Button variant="outlined" icon="download" onPress={() => api.downloadObject(projectId, o.name).catch((err: unknown) => notify(errorText.message(err)))}>
            {t('download')}
          </Button>
          {editable && (
            <Button variant="danger" icon="delete" onPress={() => setDeleting(true)}>
              {t('delete')}
            </Button>
          )}
        </div>
      </div>

      <div className={styles.columns}>
        <Card title={t('details')}>
          <DetailList>
            <Detail label={t('fullName')} value={o.name} copyLabel={t('copyName')} />
            <Detail label={t('location')} value={`gs://${bucket}/${o.name}`} copyLabel={t('copyLocation')} />
            <Detail label={t('size')}>{bytes(o.size)}</Detail>
            <Detail label={t('type')}>{o.contentType ? <Ltr>{o.contentType}</Ltr> : t('noType')}</Detail>
            <Detail label={t('created')}>
              <Time value={o.timeCreated} />
            </Detail>
            <Detail label={t('updated')}>
              <Time value={o.updated} />
            </Detail>
            <Detail label="MD5" value={o.md5Hash} />
            <Detail label="CRC32C" value={o.crc32c} />
            <Detail label={t('generation')} value={o.generation} />
          </DetailList>
        </Card>
        {isRasterImage(o.contentType) && (
          <Card title={t('preview')}>
            {/* An image's bytes from the console download, in an <img> only: never a document. */}
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img className={styles.preview} src={`${api.downloadPath(projectId, o.name)}&generation=${o.generation}`} alt={t('previewAlt', { name: o.name })} />
          </Card>
        )}
      </div>

      <TokensCard object={o} editable={editable} onChange={object.setData} />
      {/* Saved metadata starts the form again; a token change leaves an edit in progress alone. */}
      <MetadataCard
        key={JSON.stringify([api.CONTENT_FIELDS.map((f) => o[f] ?? ''), o.metadata])}
        object={o}
        editable={editable}
        onSaved={object.setData}
      />

      {deleting && (
        <ConfirmDialog
          title={t('deleteTitle')}
          confirmLabel={t('deleteConfirm')}
          danger
          onClose={() => setDeleting(false)}
          onConfirm={async () => {
            await api.deleteObject(projectId, o.name);
            notify(t('deleted', { name: o.name }));
            router.push(routes.storage(projectId, folder));
          }}
        >
          <p>{t.rich('deleteBody', { name: () => <Ltr>{o.name}</Ltr> })}</p>
        </ConfirmDialog>
      )}
    </>
  );
}

/**
 * The file's download links. Each carries a token that reads the file whatever the rules say,
 * so a link is revoked by deleting its token; deleting the last one makes another at once.
 */
function TokensCard({ object, editable, onChange }: { object: api.StoredObject; editable: boolean; onChange: (o: api.StoredObject) => void }) {
  const t = useTranslations('storage.object');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const [pending, setPending] = useState(false);
  const [revoking, setRevoking] = useState<string | null>(null);

  async function create() {
    setPending(true);
    try {
      onChange(await api.createToken(project.projectId, object.name));
      notify(t('tokenCreated'));
    } catch (err) {
      notify(errorText.message(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <Card
      title={t('tokens')}
      description={t('tokensDescription')}
      actions={
        editable && (
          <Button variant="outlined" icon="add" isPending={pending} onPress={() => void create()}>
            {t('createToken')}
          </Button>
        )
      }
    >
      {object.downloadUrls.length === 0 ? (
        <p className={styles.muted}>{t('noTokens')}</p>
      ) : (
        <ul className={styles.tokens} aria-label={t('tokens')}>
          {object.downloadUrls.map((url, i) => (
            <li key={url} className={styles.token}>
              <span className={styles.url}>
                <Ltr>{url}</Ltr>
              </span>
              <span className={styles.tokenActions}>
                <CopyButton value={url} label={t('copyLink', { n: i + 1 })} />
                {editable && <IconButton icon="delete" label={t('revoke', { n: i + 1 })} onPress={() => setRevoking(object.downloadTokens[i] ?? null)} />}
              </span>
            </li>
          ))}
        </ul>
      )}
      {revoking !== null && (
        <ConfirmDialog
          title={t('revokeTitle')}
          confirmLabel={t('revokeConfirm')}
          danger
          onClose={() => setRevoking(null)}
          onConfirm={async () => {
            onChange(await api.deleteToken(project.projectId, object.name, revoking));
            notify(t('tokenRevoked'));
          }}
        >
          <p>{object.downloadTokens.length === 1 ? t('revokeLast') : t('revokeBody')}</p>
        </ConfirmDialog>
      )}
    </Card>
  );
}

/** The file's content fields and custom metadata, saved as one PATCH of what changed. */
function MetadataCard({ object, editable, onSaved }: { object: api.StoredObject; editable: boolean; onSaved: (o: api.StoredObject) => void }) {
  const t = useTranslations('storage.object');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const { limits } = useStorageSection();
  const bytes = useBytes();
  const [form, setForm] = useState<MetadataForm>(() => formOf(object));
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [touched, setTouched] = useState(false);
  const nextRow = useRef(form.rows.length + 1);
  const patch = patchOf(object, form);
  const changed = Object.keys(patch).length > 0;
  const problems = problemsOf(form, limits ?? FALLBACK_LIMITS);

  async function save(event: FormEvent) {
    event.preventDefault();
    setTouched(true);
    if (hasProblems(problems) || !changed) return;
    setPending(true);
    setError(null);
    try {
      onSaved(await api.updateObject(project.projectId, object.name, patch));
      notify(t('metadataSaved'));
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  const setField = (f: api.ContentField, v: string) => setForm((cur) => ({ ...cur, fields: { ...cur.fields, [f]: v } }));
  const setRow = (id: number, patch: { key?: string; value?: string }) =>
    setForm((cur) => ({ ...cur, rows: cur.rows.map((r) => (r.id === id ? { ...r, ...patch } : r)) }));
  const fieldError = (f: api.ContentField) => {
    const p = touched || form.fields[f] !== (object[f] ?? '') ? problems.fields[f] : undefined;
    return p && t(`field.${p}`, { max: bytes(limits?.headerFieldBytes ?? FALLBACK_LIMITS.headerFieldBytes) });
  };

  return (
    <Card title={t('metadata')} description={t('metadataDescription')}>
      <form className={styles.form} onSubmit={save} noValidate>
        <div className={styles.fields}>
          {api.CONTENT_FIELDS.map((f) => (
            <TextField
              key={f}
              label={t(`fields.${f}`)}
              value={form.fields[f]}
              onChange={(v) => setField(f, v)}
              isReadOnly={!editable}
              ltr
              errorMessage={fieldError(f)}
            />
          ))}
        </div>
        <fieldset className={styles.custom}>
          <legend className={styles.legend}>{t('custom')}</legend>
          {form.rows.length === 0 && <p className={styles.muted}>{t('noCustom')}</p>}
          {form.rows.map((r, i) => {
            const p = problems.rows[r.id];
            const shown = p && (touched || r.key !== '') ? t(`row.${p}`) : undefined;
            return (
              <div key={r.id} className={styles.customRow}>
                <TextField label={t('key', { n: i + 1 })} value={r.key} onChange={(key) => setRow(r.id, { key })} isReadOnly={!editable} ltr errorMessage={shown} />
                <TextField label={t('value', { n: i + 1 })} value={r.value} onChange={(value) => setRow(r.id, { value })} isReadOnly={!editable} ltr />
                {editable && (
                  <IconButton
                    icon="delete"
                    label={t('removeKey', { n: i + 1 })}
                    onPress={() => setForm((cur) => ({ ...cur, rows: cur.rows.filter((x) => x.id !== r.id) }))}
                  />
                )}
              </div>
            );
          })}
          {problems.tooLarge && <Banner tone="error">{t('tooLarge', { max: bytes(limits?.metadataBytes ?? FALLBACK_LIMITS.metadataBytes) })}</Banner>}
          {editable && (
            <div>
              <Button variant="text" icon="add" onPress={() => setForm((cur) => ({ ...cur, rows: [...cur.rows, { id: nextRow.current++, key: '', value: '' }] }))}>
                {t('addKey')}
              </Button>
            </div>
          )}
        </fieldset>
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
        {editable && (
          <div className={styles.actions}>
            <Button type="submit" isPending={pending} isDisabled={!changed}>
              {tCommon('save')}
            </Button>
            <Button variant="text" icon="undo" isDisabled={!changed || pending} onPress={() => setForm(formOf(object))}>
              {t('revert')}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}
