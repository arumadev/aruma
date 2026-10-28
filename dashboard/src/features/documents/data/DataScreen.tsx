'use client';

import { useRouter, useSearchParams } from 'next/navigation';
import { useState, type ReactNode } from 'react';
import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { Icon } from '@/components/ui/Icon';
import { Menu, MenuItem } from '@/components/ui/Menu';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { useProject } from '@/features/project/ProjectContext';
import { ApiError } from '@/lib/api/client';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { useLocale } from '@/lib/i18n/LocaleProvider';
import { routes } from '@/lib/routes';
import * as api from '../api';
import { useDocumentsSection } from '../DocumentsSection';
import {
  AddDocumentDialog,
  DeleteCollectionDialog,
  DeleteDocumentDialog,
  DeleteFieldDialog,
  DeleteFieldsDialog,
  deleteField,
  FieldDialog,
  StartCollectionDialog,
  useWriteErrorText,
} from './Dialogs';
import styles from './DataScreen.module.css';
import { arrayValues, displayPath, formatValue, mapFields, normalizePath, panels, sortedEntries, splitPath, typeOf } from './values';

type Open =
  | { kind: 'startCollection'; parent: string }
  | { kind: 'addDocument'; collection: string }
  | { kind: 'field'; doc: api.Doc; path?: string[]; reload: () => void }
  | { kind: 'deleteField'; doc: api.Doc; path: string[] }
  | { kind: 'deleteDocument'; path: string }
  | { kind: 'deleteFields'; doc: api.Doc }
  | { kind: 'deleteCollection'; path: string };

/** What a column's actions open, and the project they act on. */
interface Actions {
  projectId: string;
  editable: boolean;
  open: (o: Open) => void;
  /** A change made: every column loads again. */
  rev: number;
}

const leaf = (path: string) => path.split('/').at(-1) ?? path;
const parentOf = (path: string) => splitPath(path).slice(0, -1).join('/');

/**
 * The data browser, in the Firebase console's panel view: the root's collections (or a document's),
 * the documents of the collection selected, and the fields of the document selected. The address
 * holds the path, so a reload or a link opens the same panels.
 */
export function DataScreen() {
  const t = useTranslations('documents.data');
  const notify = useSnackbar();
  const router = useRouter();
  const { project } = useProject();
  const { database, info } = useDocumentsSection();
  const projectId = project.projectId;
  const editable = project.state === 'ACTIVE';
  const path = normalizePath(useSearchParams().get('path') ?? '') ?? '';
  const layout = panels(path);
  const [rev, setRev] = useState(0);
  const [dialog, setDialog] = useState<Open | null>(null);
  useDocumentTitle(path ? `${t('title')}: /${path}` : t('title'));

  const refresh = () => setRev((r) => r + 1);
  const go = (to: string) => router.push(routes.documents(projectId, to));
  const close = () => setDialog(null);
  const actions: Actions = {
    projectId,
    editable,
    rev,
    open: setDialog,
  };

  return (
    <>
      <div className={styles.toolbar}>
        <PathBar projectId={projectId} path={path} onGo={go} />
        <Button variant="outlined" icon="rotate" onPress={refresh}>
          {t('refresh')}
        </Button>
      </div>
      {!editable && <Banner tone="info">{t('readOnly')}</Banner>}

      <div className={styles.panels} role="group" aria-label={t('columns')}>
        <div className={styles.panel}>
          {layout.parent === '' ? (
            <RootColumn databaseId={info.data?.databaseId ?? '(default)'} selected={layout.collection} actions={actions} />
          ) : (
            <DocumentColumn key={layout.parent} path={layout.parent} selected={layout.collection} actions={actions} />
          )}
        </div>
        <div className={styles.panel}>
          {layout.collection ? (
            <CollectionColumn key={layout.collection} path={layout.collection} selected={layout.document} actions={actions} />
          ) : (
            <p className={styles.hint}>{t('selectCollection')}</p>
          )}
        </div>
        <div className={styles.panel}>
          {layout.document ? (
            <DocumentColumn key={layout.document} path={layout.document} actions={actions} />
          ) : (
            layout.collection && <p className={styles.hint}>{t('selectDocument')}</p>
          )}
        </div>
      </div>

      {dialog?.kind === 'startCollection' && (
        <StartCollectionDialog
          projectId={projectId}
          database={database}
          parentPath={dialog.parent}
          onClose={close}
          onCreated={(doc) => {
            close();
            notify(t('added', { id: doc.id }));
            refresh();
            go(doc.path);
          }}
        />
      )}
      {dialog?.kind === 'addDocument' && (
        <AddDocumentDialog
          projectId={projectId}
          database={database}
          collectionPath={dialog.collection}
          onClose={close}
          onCreated={(doc) => {
            close();
            notify(t('added', { id: doc.id }));
            refresh();
            go(doc.path);
          }}
        />
      )}
      {dialog?.kind === 'field' && (
        <FieldDialog
          projectId={projectId}
          database={database}
          doc={dialog.doc}
          path={dialog.path}
          onClose={close}
          onReload={dialog.reload}
          onSaved={(_, name) => {
            close();
            notify(t('saved', { name }));
            refresh();
          }}
        />
      )}
      {dialog?.kind === 'deleteField' && (
        <DeleteFieldDialog
          doc={dialog.doc}
          path={dialog.path}
          onClose={close}
          onConfirm={async () => {
            await deleteField(projectId, dialog.doc, dialog.path);
            notify(t('fieldDeleted', { name: displayPath(dialog.path) }));
            refresh();
          }}
        />
      )}
      {dialog?.kind === 'deleteFields' && (
        <DeleteFieldsDialog
          projectId={projectId}
          doc={dialog.doc}
          onClose={close}
          onDeleted={() => {
            notify(t('fieldsDeleted', { id: dialog.doc.id }));
            refresh();
          }}
        />
      )}
      {dialog?.kind === 'deleteDocument' && (
        <DeleteDocumentDialog
          projectId={projectId}
          path={dialog.path}
          onClose={close}
          onDeleted={() => {
            notify(t('deleted', { path: `/${dialog.path}` }));
            refresh();
            go(parentOf(dialog.path));
          }}
        />
      )}
      {dialog?.kind === 'deleteCollection' && (
        <DeleteCollectionDialog
          projectId={projectId}
          path={dialog.path}
          onClose={close}
          onDeleted={() => {
            notify(t('deleted', { path: `/${dialog.path}` }));
            refresh();
            go(parentOf(dialog.path));
          }}
        />
      )}
    </>
  );
}

/** The breadcrumb of the path, and its "Edit path" box. */
function PathBar({ projectId, path, onGo }: { projectId: string; path: string; onGo: (path: string) => void }) {
  const t = useTranslations('documents.data');
  const tCommon = useTranslations('common');
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState('');
  const [invalid, setInvalid] = useState(false);
  const ids = splitPath(path);

  if (editing) {
    return (
      <form
        className={styles.pathForm}
        onSubmit={(e) => {
          e.preventDefault();
          const next = normalizePath(text);
          if (next === undefined) {
            setInvalid(true);
            return;
          }
          setEditing(false);
          onGo(next);
        }}
      >
        <TextField
          label={t('path')}
          value={text}
          onChange={(v) => {
            setText(v);
            setInvalid(false);
          }}
          ltr
          autoFocus
          className={styles.pathInput}
          errorMessage={invalid ? t('pathInvalid') : undefined}
        />
        <Button type="submit">{t('go')}</Button>
        <Button variant="text" onPress={() => setEditing(false)}>
          {tCommon('cancel')}
        </Button>
      </form>
    );
  }
  return (
    <div className={styles.pathBar}>
      <nav aria-label={t('breadcrumbs')} className={styles.crumbs}>
        <ol className={styles.crumbList}>
          <li>
            {ids.length ? (
              <Link className={styles.crumb} href={routes.documents(projectId)} aria-label={t('root')}>
                <Icon name="home" size={18} />
              </Link>
            ) : (
              <span className={styles.current} aria-current="page" aria-label={t('root')}>
                <Icon name="home" size={18} />
              </span>
            )}
          </li>
          {ids.map((id, i) => (
            <li key={i}>
              <span className={styles.separator} aria-hidden="true">
                /
              </span>
              {i === ids.length - 1 ? (
                <span className={styles.current} aria-current="page">
                  {id}
                </span>
              ) : (
                <Link className={styles.crumb} href={routes.documents(projectId, ids.slice(0, i + 1).join('/'))}>
                  {id}
                </Link>
              )}
            </li>
          ))}
        </ol>
      </nav>
      <IconButton
        icon="edit"
        label={t('editPath')}
        onPress={() => {
          setText(`/${path}`);
          setEditing(true);
        }}
      />
    </div>
  );
}

/** A list read a page at a time, with "Load more". */
function usePaged<T>(load: (token: string | undefined, signal?: AbortSignal) => Promise<{ items: T[]; next?: string }>, deps: (string | number)[]) {
  const first = useResource((signal) => load(undefined, signal), deps);
  const [more, setMore] = useState<{ loading: boolean; error?: unknown }>({ loading: false });
  async function loadMore() {
    const token = first.data?.next;
    if (!token) return;
    setMore({ loading: true });
    try {
      const page = await load(token);
      first.setData((cur) => ({ items: [...(cur?.items ?? []), ...page.items], next: page.next }));
      setMore({ loading: false });
    } catch (error) {
      setMore({ loading: false, error });
    }
  }
  return { ...first, more, loadMore };
}

function LoadMore({ paged }: { paged: ReturnType<typeof usePaged> }) {
  const t = useTranslations('documents.data');
  const errorText = useErrorText();
  if (!paged.data?.next) return null;
  return (
    <div className={styles.more}>
      {Boolean(paged.more.error) && <Banner tone="error">{errorText.message(paged.more.error)}</Banner>}
      <Button variant="text" isPending={paged.more.loading} onPress={() => void paged.loadMore()}>
        {t('loadMore')}
      </Button>
    </div>
  );
}

function LoadError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  return (
    <Banner tone="error" action={<Button variant="text" onPress={onRetry}>{tCommon('retry')}</Button>}>
      {errorText.message(error)}
    </Banner>
  );
}

/** The collections under the root or a document, as links; the selected one marked. */
function CollectionList({ parent, selected, actions }: { parent: string; selected?: string; actions: Actions }) {
  const t = useTranslations('documents.data');
  const { limits } = useDocumentsSection();
  const paged = usePaged(
    async (pageToken, signal) => {
      const page = await api.listCollections(actions.projectId, parent, { pageSize: limits?.collectionsPageSize, pageToken }, signal);
      return { items: page.collectionIds, next: page.nextPageToken };
    },
    [actions.projectId, parent, actions.rev],
  );
  const name = parent ? leaf(parent) : '/';
  if (paged.error && !paged.data) return <LoadError error={paged.error} onRetry={paged.reload} />;
  if (!paged.data) return <Skeleton lines={2} />;
  return (
    <>
      {paged.data.items.length === 0 ? (
        <p className={styles.hint}>{t('noCollections')}</p>
      ) : (
        <ul className={styles.list} aria-label={t('collections', { name })}>
          {paged.data.items.map((id) => {
            const path = parent ? `${parent}/${id}` : id;
            return (
              <li key={id}>
                <Link
                  className={styles.item}
                  href={routes.documents(actions.projectId, path)}
                  aria-current={selected === path ? 'page' : undefined}
                >
                  <Icon name="layers" size={18} />
                  <Ltr mono={false}>{id}</Ltr>
                </Link>
              </li>
            );
          })}
        </ul>
      )}
      <LoadMore paged={paged as ReturnType<typeof usePaged>} />
    </>
  );
}

function ColumnHeader({ icon, title, menu }: { icon: 'home' | 'layers' | 'description'; title: string; menu?: ReactNode }) {
  return (
    <div className={styles.header}>
      <Icon name={icon} size={20} />
      <h2 className={styles.title}>
        <Ltr mono={false}>{title}</Ltr>
      </h2>
      {menu}
    </div>
  );
}

/** The root: the database, "Start collection" and its collections. */
function RootColumn({ databaseId, selected, actions }: { databaseId: string; selected?: string; actions: Actions }) {
  const t = useTranslations('documents.data');
  return (
    <section aria-label={databaseId} className={styles.column}>
      <ColumnHeader icon="home" title={databaseId} />
      {actions.editable && (
        <Button variant="text" icon="add" onPress={() => actions.open({ kind: 'startCollection', parent: '' })}>
          {t('startCollection')}
        </Button>
      )}
      <CollectionList parent="" selected={selected} actions={actions} />
    </section>
  );
}

/** A collection: its menu, "Add document" and its documents, a page at a time, the missing ones in italics. */
function CollectionColumn({ path, selected, actions }: { path: string; selected?: string; actions: Actions }) {
  const t = useTranslations('documents.data');
  const id = leaf(path);
  const paged = usePaged(
    async (pageToken, signal) => {
      const page = await api.listDocuments(actions.projectId, path, { pageToken, fields: false }, signal);
      return { items: page.documents, next: page.nextPageToken };
    },
    [actions.projectId, path, actions.rev],
  );
  const menu = actions.editable && (
    <Menu label={t('collectionMenu', { id })} trigger={<IconButton icon="moreVert" label={t('collectionMenu', { id })} />}>
      <MenuItem icon="delete" danger onAction={() => actions.open({ kind: 'deleteCollection', path })}>
        {t('deleteCollection')}
      </MenuItem>
    </Menu>
  );
  return (
    <section aria-label={id} className={styles.column}>
      <ColumnHeader icon="layers" title={id} menu={menu} />
      {actions.editable && (
        <Button variant="text" icon="add" onPress={() => actions.open({ kind: 'addDocument', collection: path })}>
          {t('addDocument')}
        </Button>
      )}
      {paged.error && !paged.data ? (
        <LoadError error={paged.error} onRetry={paged.reload} />
      ) : !paged.data ? (
        <Skeleton lines={3} />
      ) : paged.data.items.length === 0 ? (
        <p className={styles.hint}>{t('noDocuments')}</p>
      ) : (
        <ul className={styles.list} aria-label={t('documents', { name: id })}>
          {paged.data.items.map((d) => (
            <li key={d.path}>
              <Link
                className={[styles.item, d.missing && styles.missing].filter(Boolean).join(' ')}
                href={routes.documents(actions.projectId, d.path)}
                aria-current={selected === d.path ? 'page' : undefined}
                aria-label={d.missing ? t('missingLabel', { id: d.id }) : undefined}
              >
                <Ltr mono={false}>{d.id}</Ltr>
              </Link>
            </li>
          ))}
        </ul>
      )}
      <LoadMore paged={paged as ReturnType<typeof usePaged>} />
    </section>
  );
}

/** A document: its menu, its subcollections and its fields; a missing one says it does not exist. */
function DocumentColumn({ path, selected, actions }: { path: string; selected?: string; actions: Actions }) {
  const t = useTranslations('documents.data');
  const id = leaf(path);
  const doc = useResource(
    (signal) =>
      api.getDocument(actions.projectId, path, signal).catch((err: unknown) => {
        if (err instanceof ApiError && err.status === 'NOT_FOUND') return null;
        throw err;
      }),
    [actions.projectId, path, actions.rev],
  );
  const d = doc.data;
  const menu = actions.editable && (
    <Menu label={t('documentMenu', { id })} trigger={<IconButton icon="moreVert" label={t('documentMenu', { id })} />}>
      <MenuItem icon="delete" danger onAction={() => actions.open({ kind: 'deleteDocument', path })}>
        {t('deleteDocument')}
      </MenuItem>
      {d ? (
        <MenuItem icon="delete" danger onAction={() => actions.open({ kind: 'deleteFields', doc: d })}>
          {t('deleteFields')}
        </MenuItem>
      ) : null}
    </Menu>
  );
  return (
    <section aria-label={id} className={styles.column}>
      <ColumnHeader icon="description" title={id} menu={menu} />
      {actions.editable && (
        <Button variant="text" icon="add" onPress={() => actions.open({ kind: 'startCollection', parent: path })}>
          {t('startCollection')}
        </Button>
      )}
      <CollectionList parent={path} selected={selected} actions={actions} />
      <hr className={styles.rule} />
      {doc.error && d === undefined ? (
        <LoadError error={doc.error} onRetry={doc.reload} />
      ) : d === undefined ? (
        <Skeleton lines={3} />
      ) : d === null ? (
        <p className={`${styles.hint} ${styles.missing}`}>{t('missing')}</p>
      ) : (
        <>
          {actions.editable && (
            <Button variant="text" icon="add" onPress={() => actions.open({ kind: 'field', doc: d, reload: doc.reload })}>
              {t('addField')}
            </Button>
          )}
          {sortedEntries(d.fields).length === 0 ? (
            <p className={styles.hint}>{t('noFields')}</p>
          ) : (
            <FieldList doc={d} entries={sortedEntries(d.fields)} path={[]} actions={actions} reload={doc.reload} label={t('fields', { name: id })} />
          )}
        </>
      )}
    </section>
  );
}

function FieldList({
  doc,
  entries,
  path,
  actions,
  reload,
  label,
  inArray,
}: {
  doc: api.Doc;
  /** A map's fields by name, or an array's items by index. */
  entries: [string, api.Value][];
  path: string[];
  actions: Actions;
  reload: () => void;
  label: string;
  inArray?: boolean;
}) {
  return (
    <ul className={path.length ? `${styles.fields} ${styles.nested}` : styles.fields} aria-label={label}>
      {entries.map(([name, v]) => (
        <FieldNode key={name} doc={doc} name={name} value={v} path={[...path, name]} actions={actions} reload={reload} inArray={inArray} />
      ))}
    </ul>
  );
}

/** One field, "name: value (type)", with its edit and delete buttons; a map or an array opens and closes. */
function FieldNode({
  doc,
  name,
  value,
  path,
  actions,
  reload,
  inArray,
}: {
  doc: api.Doc;
  name: string;
  value: api.Value;
  path: string[];
  actions: Actions;
  reload: () => void;
  /** An array's item: the path names no field, so it is edited with its array. */
  inArray?: boolean;
}) {
  const t = useTranslations('documents.data');
  const { locale } = useLocale();
  const { database } = useDocumentsSection();
  const notify = useSnackbar();
  const writeError = useWriteErrorText();
  const [open, setOpen] = useState(true);
  const type = typeOf(value);
  const container = type === 'map' || type === 'array';
  const children: [string, api.Value][] =
    type === 'map' ? sortedEntries(mapFields(value)) : arrayValues(value).map((v, i): [string, api.Value] => [String(i), v]);
  const shown = displayPath(path);

  return (
    <li className={styles.field}>
      <div className={styles.fieldRow}>
        {container ? (
          <IconButton
            icon="chevronForward"
            label={open ? t('collapse', { name: shown }) : t('expand', { name: shown })}
            aria-expanded={open}
            className={open ? styles.open : undefined}
            onPress={() => setOpen(!open)}
          />
        ) : (
          <span className={styles.spacer} />
        )}
        <span className={styles.fieldText}>
          <bdi className={styles.fieldName}>{name}</bdi>
          {container ? null : (
            <>
              {': '}
              <bdi className={styles.fieldValue} dir="auto">
                {formatValue(value, { locale, database })}
              </bdi>
            </>
          )}{' '}
          <span className={styles.fieldType} dir="ltr">
            {t('typeLabel', { type })}
          </span>
        </span>
        {actions.editable && !inArray && (
          <span className={styles.fieldActions}>
            {type !== 'bytes' && (
              <IconButton icon="edit" label={t('editField', { name: shown })} onPress={() => actions.open({ kind: 'field', doc, path, reload })} />
            )}
            <IconButton
              icon="delete"
              label={t('deleteField', { name: shown })}
              onPress={(e) => {
                if (!e.shiftKey) {
                  actions.open({ kind: 'deleteField', doc, path });
                  return;
                }
                deleteField(actions.projectId, doc, path).then(
                  () => {
                    notify(t('fieldDeleted', { name: shown }));
                    reload();
                  },
                  (err: unknown) => notify(writeError(err)),
                );
              }}
            />
          </span>
        )}
      </div>
      {container && open && (
        <FieldList
          doc={doc}
          entries={children}
          path={path}
          actions={actions}
          reload={reload}
          label={shown}
          inArray={inArray || type === 'array'}
          key={type}
        />
      )}
    </li>
  );
}
