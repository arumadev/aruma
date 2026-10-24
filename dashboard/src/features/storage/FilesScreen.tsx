'use client';

import { useRouter, useSearchParams } from 'next/navigation';
import { useState } from 'react';
import { FileTrigger, Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Dialog } from '@/components/ui/Dialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { Icon } from '@/components/ui/Icon';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import * as api from './api';
import { useBytes } from './Bytes';
import styles from './FilesScreen.module.css';
import { crumbs, folderProblem, leafName, nameProblem, type NameProblem } from './paths';
import { useStorageSection } from './StorageSection';
import { UploadList, useUploads } from './Uploads';

/** Entries per page of a folder; "Load more" fetches the next. */
export const FILES_PAGE_SIZE = 100;

interface Picked {
  name: string;
  file: File;
}

/**
 * The bucket's files a folder at a time: subfolders, then the files directly in the folder. Files
 * are uploaded into the folder shown, a resumable upload each, and deleted after a confirmation.
 */
export function FilesScreen() {
  const t = useTranslations('storage.files');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const bytes = useBytes();
  const router = useRouter();
  const { project } = useProject();
  const { bucket, limits } = useStorageSection();
  const projectId = project.projectId;
  const editable = project.state === 'ACTIVE';
  const prefix = normalizePrefix(useSearchParams().get('prefix') ?? '');
  const listing = useResource((signal) => api.browse(projectId, prefix, { pageSize: FILES_PAGE_SIZE }, signal), [projectId, prefix]);
  const [more, setMore] = useState<{ loading: boolean; error?: unknown }>({ loading: false });
  const [deleting, setDeleting] = useState<string | null>(null);
  const [replacing, setReplacing] = useState<Picked[] | null>(null);
  const [refused, setRefused] = useState<{ name: string; problem: NameProblem | 'size' }[]>([]);
  const [newFolder, setNewFolder] = useState(false);
  useDocumentTitle(prefix ? `${t('title')}: ${prefix}` : t('title'));

  const uploads = useUploads(projectId, (name) => {
    notify(t('uploaded', { name }));
    if (name.startsWith(prefix)) listing.reload();
  });

  async function loadMore() {
    const token = listing.data?.nextPageToken;
    if (!token) return;
    setMore({ loading: true });
    try {
      const page = await api.browse(projectId, prefix, { pageSize: FILES_PAGE_SIZE, pageToken: token });
      listing.setData((cur) => ({
        prefixes: [...(cur?.prefixes ?? []), ...page.prefixes],
        items: [...(cur?.items ?? []), ...page.items],
        nextPageToken: page.nextPageToken,
      }));
      setMore({ loading: false });
    } catch (error) {
      setMore({ loading: false, error });
    }
  }

  function pick(files: FileList | null) {
    if (!files) return;
    const ok: Picked[] = [];
    const bad: typeof refused = [];
    for (const file of Array.from(files)) {
      const name = prefix + file.name;
      const problem = nameProblem(name) ?? (limits && file.size > limits.objectBytes ? 'size' : undefined);
      if (problem) bad.push({ name, problem });
      else ok.push({ name, file });
    }
    setRefused(bad);
    if (ok.length === 0) return;
    const existing = new Set((listing.data?.items ?? []).map((o) => o.name));
    if (ok.some((p) => existing.has(p.name))) setReplacing(ok);
    else uploads.add(ok);
  }

  const data = listing.data;
  const empty = data && data.prefixes.length === 0 && data.items.length === 0;
  const path = crumbs(prefix);

  const uploadButton = editable && (
    <FileTrigger allowsMultiple onSelect={pick}>
      <Button icon="upload">{t('upload')}</Button>
    </FileTrigger>
  );

  return (
    <>
      <div className={styles.toolbar}>
        <nav aria-label={t('breadcrumbs')} className={styles.crumbs}>
          <ol className={styles.crumbList}>
            <li>
              {prefix ? (
                <Link className={styles.crumb} href={routes.storage(projectId)}>
                  <Ltr>{bucket}</Ltr>
                </Link>
              ) : (
                <span className={styles.current} aria-current="page">
                  <Ltr>{bucket}</Ltr>
                </span>
              )}
            </li>
            {path.map((c, i) => (
              <li key={c.prefix}>
                <span className={styles.separator} aria-hidden="true">
                  /
                </span>
                {i === path.length - 1 ? (
                  <span className={styles.current} aria-current="page">
                    <Ltr>{c.label}</Ltr>
                  </span>
                ) : (
                  <Link className={styles.crumb} href={routes.storage(projectId, c.prefix)}>
                    <Ltr>{c.label}</Ltr>
                  </Link>
                )}
              </li>
            ))}
          </ol>
        </nav>
        <div className={styles.buttons}>
          <Button variant="outlined" icon="rotate" onPress={listing.reload}>
            {t('refresh')}
          </Button>
          {editable && (
            <Button variant="outlined" icon="add" onPress={() => setNewFolder(true)}>
              {t('newFolder')}
            </Button>
          )}
          {uploadButton}
        </div>
      </div>

      {refused.length > 0 && (
        <Banner tone="error" action={<Button variant="text" onPress={() => setRefused([])}>{tCommon('dismiss')}</Button>}>
          <ul className={styles.refused}>
            {refused.map((r) => (
              <li key={r.name}>
                <Ltr>{r.name}</Ltr> — {r.problem === 'size' ? t('tooLarge', { max: bytes(limits?.objectBytes ?? 0) }) : t(`name.${r.problem}`)}
              </li>
            ))}
          </ul>
        </Banner>
      )}

      <UploadList uploads={uploads.uploads} onCancel={uploads.cancel} onRetry={uploads.retry} onClear={uploads.clearFinished} />

      {listing.error && !data ? (
        <Banner tone="error" action={<Button variant="text" onPress={listing.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(listing.error)}
        </Banner>
      ) : !data ? (
        <Skeleton lines={4} />
      ) : empty ? (
        <EmptyState icon="folder" title={prefix ? t('emptyFolderTitle') : t('emptyTitle')} action={uploadButton}>
          {prefix ? t('emptyFolderBody') : t('emptyBody')}
        </EmptyState>
      ) : (
        <Table caption={t('table', { folder: prefix || bucket })} columns={[t('column.name'), t('column.size'), t('column.type'), t('column.updated'), t('column.actions')]}>
          {data.prefixes.map((p) => (
            <Row key={p}>
              <Cell>
                <Link className={styles.entry} href={routes.storage(projectId, p)} aria-label={t('openFolder', { name: leafName(p, prefix) })}>
                  <Icon name="folder" size={20} />
                  <Ltr>{`${leafName(p, prefix)}/`}</Ltr>
                </Link>
              </Cell>
              <Cell>—</Cell>
              <Cell>{t('folder')}</Cell>
              <Cell>—</Cell>
              <Cell actions />
            </Row>
          ))}
          {data.items.map((o) => (
            <Row key={o.name}>
              <Cell>
                <Link className={styles.entry} href={routes.storageObject(projectId, o.name)}>
                  <Icon name="description" size={20} />
                  <Ltr>{leafName(o.name, prefix) || o.name}</Ltr>
                </Link>
              </Cell>
              <Cell nowrap>{bytes(o.size)}</Cell>
              <Cell>{o.contentType ? <Ltr mono={false}>{o.contentType}</Ltr> : '—'}</Cell>
              <Cell nowrap>
                <Time value={o.updated} />
              </Cell>
              <Cell actions>
                <IconButton
                  icon="download"
                  label={t('download', { name: o.name })}
                  onPress={() => api.downloadObject(projectId, o.name).catch((err: unknown) => notify(errorText.message(err)))}
                />
                {editable && <IconButton icon="delete" label={t('delete', { name: o.name })} onPress={() => setDeleting(o.name)} />}
              </Cell>
            </Row>
          ))}
        </Table>
      )}
      {data?.nextPageToken && (
        <div className={styles.more}>
          {Boolean(more.error) && <Banner tone="error">{errorText.message(more.error)}</Banner>}
          <Button variant="text" isPending={more.loading} onPress={() => void loadMore()}>
            {t('loadMore')}
          </Button>
        </div>
      )}

      {deleting !== null && (
        <ConfirmDialog
          title={t('deleteTitle')}
          confirmLabel={t('deleteConfirm')}
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await api.deleteObject(projectId, deleting);
            notify(t('deleted', { name: deleting }));
            listing.setData((cur) => ({ prefixes: cur?.prefixes ?? [], items: (cur?.items ?? []).filter((o) => o.name !== deleting), nextPageToken: cur?.nextPageToken }));
          }}
        >
          <p>{t.rich('deleteBody', { name: () => <Ltr>{deleting}</Ltr> })}</p>
        </ConfirmDialog>
      )}
      {replacing && (
        <ConfirmDialog
          title={t('replaceTitle', { count: replacing.length })}
          confirmLabel={t('replaceConfirm')}
          onClose={() => setReplacing(null)}
          onConfirm={async () => {
            uploads.add(replacing);
          }}
        >
          <p>{t('replaceBody')}</p>
          <ul className={styles.replaceList}>
            {replacing
              .filter((p) => data?.items.some((o) => o.name === p.name))
              .map((p) => (
                <li key={p.name}>
                  <Ltr>{p.name}</Ltr>
                </li>
              ))}
          </ul>
        </ConfirmDialog>
      )}
      {newFolder && (
        <NewFolderDialog
          onClose={() => setNewFolder(false)}
          onCreate={(name) => {
            setNewFolder(false);
            router.push(routes.storage(projectId, `${prefix}${name}/`));
          }}
        />
      )}
    </>
  );
}

/** A folder from the address: "" for the root, else ending in "/". */
function normalizePrefix(prefix: string): string {
  return prefix === '' || prefix.endsWith('/') ? prefix : `${prefix}/`;
}

/** Names a folder to open. It exists once a file is uploaded into it, as in any bucket. */
function NewFolderDialog({ onClose, onCreate }: { onClose: () => void; onCreate: (name: string) => void }) {
  const t = useTranslations('storage.files');
  const tCommon = useTranslations('common');
  const [name, setName] = useState('');
  const [touched, setTouched] = useState(false);
  const problem = folderProblem(name);
  return (
    <Dialog
      isOpen
      onOpenChange={(open) => !open && onClose()}
      title={t('newFolderTitle')}
      onSubmit={() => {
        setTouched(true);
        if (!problem) onCreate(name);
      }}
      actions={
        <>
          <Button variant="text" onPress={onClose}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit">{t('newFolderConfirm')}</Button>
        </>
      }
    >
      <p>{t('newFolderBody')}</p>
      <TextField
        label={t('folderName')}
        value={name}
        onChange={setName}
        ltr
        autoFocus
        errorMessage={touched && problem ? t(`name.${problem}`) : undefined}
      />
    </Dialog>
  );
}
