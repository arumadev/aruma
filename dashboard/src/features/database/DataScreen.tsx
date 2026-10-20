'use client';

import { useCallback, useEffect, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { DataChild, LeafValue } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useDatabaseSection } from './DatabaseSection';
import styles from './DataScreen.module.css';
import { NodeEditor } from './NodeEditor';
import { childPath, formatValueInput, parentPath } from './validation';

/** Children loaded per page; "Load more" fetches the next. */
export const DATA_PAGE_SIZE = 50;

/** One node of the tree as loaded so far. */
interface NodeState {
  loading: boolean;
  loadingMore?: boolean;
  error?: unknown;
  /** The node itself when it is a leaf (only the root is shown this way). */
  value?: LeafValue;
  children: DataChild[];
  next: string;
}

const EMPTY: NodeState = { loading: true, children: [], next: '' };

type Editing = { mode: 'edit' | 'add'; path: string; initial?: string } | null;

/**
 * The project's data as a tree that opens a level at a time: big lists come a page at a time,
 * leaves are edited in place, objects take new children, and deletes ask first. The data is
 * read when a node opens and again on Refresh: changes made elsewhere do not stream in.
 */
export function DataScreen() {
  const t = useTranslations('database.data');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const { info, limits } = useDatabaseSection();
  const projectId = project.projectId;
  const editable = project.state === 'ACTIVE';
  const [nodes, setNodes] = useState<Record<string, NodeState>>({});
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const [editing, setEditing] = useState<Editing>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  useDocumentTitle(t('title'));

  const fetchPage = useCallback(
    async (path: string, pageToken?: string) => {
      setNodes((prev) => ({
        ...prev,
        [path]: pageToken ? { ...(prev[path] ?? EMPTY), loadingMore: true, error: undefined } : { ...(prev[path] ?? EMPTY), loading: true, error: undefined },
      }));
      try {
        const page = await api.browseData(projectId, path, { pageSize: DATA_PAGE_SIZE, pageToken });
        setNodes((prev) => ({
          ...prev,
          [path]: {
            loading: false,
            value: page.value,
            children: pageToken ? [...(prev[path]?.children ?? []), ...page.children] : page.children,
            next: page.nextPageToken,
          },
        }));
      } catch (error) {
        setNodes((prev) => ({ ...prev, [path]: { ...(prev[path] ?? EMPTY), loading: false, loadingMore: false, error } }));
      }
    },
    [projectId],
  );

  useEffect(() => {
    const controller = new AbortController();
    api.browseData(projectId, '/', { pageSize: DATA_PAGE_SIZE }, controller.signal).then(
      (page) => setNodes({ '/': { loading: false, value: page.value, children: page.children, next: page.nextPageToken } }),
      (error: unknown) => {
        if (!controller.signal.aborted) setNodes({ '/': { ...EMPTY, loading: false, error } });
      },
    );
    return () => controller.abort();
  }, [projectId]);

  const toggle = (path: string) => {
    const next = new Set(expanded);
    if (next.has(path)) {
      next.delete(path);
    } else {
      next.add(path);
      void fetchPage(path);
    }
    setExpanded(next);
  };

  const refresh = () => {
    void fetchPage('/');
    for (const path of expanded) void fetchPage(path);
    info.reload();
  };

  const startEdit = async (path: string, child: DataChild) => {
    let value = child.value;
    if (child.truncated) {
      // The listing cuts long strings; edit the whole value.
      try {
        value = (await api.browseData(projectId, path, {})).value;
      } catch (err) {
        notify(errorText.message(err));
        return;
      }
    }
    setEditing({ mode: 'edit', path, initial: value === undefined ? '' : formatValueInput(value) });
  };

  const startAdd = (path: string) => {
    if (path !== '/' && !expanded.has(path)) toggle(path);
    setEditing({ mode: 'add', path });
  };

  const saved = (editedPath: string, mode: 'edit' | 'add', at: string) => {
    setEditing(null);
    notify(mode === 'add' ? t('added', { path: editedPath }) : t('saved', { path: editedPath }));
    void fetchPage(mode === 'add' ? at : parentPath(at));
    info.reload();
  };

  const root = nodes['/'] ?? EMPTY;
  const rootEmpty = !root.loading && !root.error && root.value === undefined && root.children.length === 0;
  const size = info.data?.size;

  async function exportNode(path: string) {
    try {
      await api.exportData(projectId, path);
    } catch (err) {
      notify(errorText.message(err));
    }
  }

  // Rows are rendered by functions, not nested components, so they keep their focus and state
  // across renders.
  const renderChildren = (path: string) => {
    const state = nodes[path] ?? EMPTY;
    const adding = editing?.mode === 'add' && editing.path === path;
    return (
      <>
        {adding && (
          <div className={styles.inlineEditor}>
            <NodeEditor
              projectId={projectId}
              mode="add"
              path={path}
              limits={limits}
              onSaved={(p) => saved(p, 'add', path)}
              onCancel={() => setEditing(null)}
            />
          </div>
        )}
        {state.error ? (
          <Banner tone="error" action={<Button variant="text" onPress={() => void fetchPage(path)}>{tCommon('retry')}</Button>}>
            {errorText.message(state.error)}
          </Banner>
        ) : state.loading && state.children.length === 0 ? (
          <Skeleton lines={2} />
        ) : (
          <ul className={styles.list} aria-label={path === '/' ? t('rootList') : t('childList', { path })}>
            {state.children.map((child) => renderRow(path, child))}
          </ul>
        )}
        {state.next && !state.error && (
          <div className={styles.more}>
            <Button variant="text" isPending={state.loadingMore} onPress={() => void fetchPage(path, state.next)}>
              {t('loadMore')}
            </Button>
          </div>
        )}
      </>
    );
  };

  const renderRow = (parent: string, child: DataChild) => {
    const path = childPath(parent, child.key);
    const open = expanded.has(path);
    const isEditing = editing?.mode === 'edit' && editing.path === path;
    return (
      <li key={child.key} className={styles.item}>
        <div className={styles.row}>
          {child.hasChildren ? (
            <IconButton
              className={open ? styles.toggleOpen : styles.toggle}
              icon="chevronForward"
              label={open ? t('collapse', { key: child.key }) : t('expand', { key: child.key })}
              aria-expanded={open}
              onPress={() => toggle(path)}
            />
          ) : (
            <span className={styles.spacer} aria-hidden="true" />
          )}
          <span className={styles.entry}>
            <bdi dir="ltr" className={styles.key}>
              {child.key}
            </bdi>
            {child.value !== undefined && !isEditing && (
              <>
                <span className={styles.colon} aria-hidden="true">
                  :
                </span>
                <LeafText value={child.value} truncated={child.truncated} />
              </>
            )}
          </span>
          {editable && !isEditing && (
            <span className={styles.actions}>
              {child.hasChildren ? (
                <>
                  <IconButton icon="add" label={t('addTo', { key: child.key })} onPress={() => startAdd(path)} />
                  <IconButton icon="download" label={t('exportNode', { key: child.key })} onPress={() => void exportNode(path)} />
                </>
              ) : (
                <IconButton icon="edit" label={t('edit', { key: child.key })} onPress={() => void startEdit(path, child)} />
              )}
              <IconButton icon="delete" label={t('delete', { key: child.key })} onPress={() => setDeleting(path)} />
            </span>
          )}
        </div>
        {isEditing && (
          <div className={styles.inlineEditor}>
            <NodeEditor
              projectId={projectId}
              mode="edit"
              path={path}
              initial={editing.initial}
              limits={limits}
              onSaved={(p) => saved(p, 'edit', path)}
              onCancel={() => setEditing(null)}
            />
          </div>
        )}
        {child.hasChildren && open && <div className={styles.nested}>{renderChildren(path)}</div>}
      </li>
    );
  };

  return (
    <>
      <div className={styles.toolbar}>
        <p className={styles.summary}>
          {size && t('stored', { count: size.rows, capped: String(size.capped) })} {t('refreshHint')}
        </p>
        <div className={styles.buttons}>
          <Button variant="outlined" icon="rotate" onPress={refresh}>
            {t('refresh')}
          </Button>
          <Button variant="outlined" icon="download" onPress={() => void exportNode('/')}>
            {t('export')}
          </Button>
          {editable && root.value === undefined && (
            <Button icon="add" onPress={() => startAdd('/')}>
              {t('addChild')}
            </Button>
          )}
        </div>
      </div>
      <section className={styles.tree} aria-label={t('tree')}>
        <div className={styles.rootRow}>
          <Ltr>{info.data?.name ?? '/'}</Ltr>
          {root.value !== undefined && editing?.path !== '/' && (
            <>
              <span className={styles.colon} aria-hidden="true">
                :
              </span>
              <LeafText value={root.value} />
              {editable && (
                <span className={styles.actions}>
                  <IconButton icon="edit" label={t('editRoot')} onPress={() => void startEdit('/', { key: '', value: root.value })} />
                  <IconButton icon="delete" label={t('deleteRoot')} onPress={() => setDeleting('/')} />
                </span>
              )}
            </>
          )}
        </div>
        {editing?.mode === 'edit' && editing.path === '/' && (
          <NodeEditor
            projectId={projectId}
            mode="edit"
            path="/"
            initial={editing.initial}
            limits={limits}
            onSaved={(p) => saved(p, 'edit', '/')}
            onCancel={() => setEditing(null)}
          />
        )}
        {rootEmpty && !(editing?.mode === 'add') ? (
          <EmptyState
            icon="bolt"
            title={t('emptyTitle')}
            action={
              editable && (
                <Button icon="add" onPress={() => startAdd('/')}>
                  {t('addChild')}
                </Button>
              )
            }
          >
            {t('emptyBody')}
          </EmptyState>
        ) : (
          root.value === undefined && renderChildren('/')
        )}
      </section>
      {deleting !== null && (
        <ConfirmDialog
          title={t('deleteTitle')}
          confirmLabel={t('deleteConfirm')}
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await api.deleteData(projectId, deleting);
            setExpanded((prev) => new Set([...prev].filter((p) => p !== deleting && !p.startsWith(`${deleting}/`))));
            notify(t('deleted', { path: deleting }));
            void fetchPage(parentPath(deleting));
            info.reload();
          }}
        >
          <p>{t.rich('deleteBody', { path: () => <Ltr>{deleting}</Ltr> })}</p>
        </ConfirmDialog>
      )}
    </>
  );
}

/** A leaf as JSON shows it: strings quoted, numbers and booleans plain, in their own colours. */
function LeafText({ value, truncated }: { value: LeafValue; truncated?: boolean }) {
  const kind = typeof value === 'string' ? styles.string : typeof value === 'number' ? styles.number : styles.boolean;
  const text = typeof value === 'string' ? `"${value}${truncated ? '…' : ''}"` : String(value);
  return (
    <bdi dir="ltr" className={`${styles.value} ${kind}`}>
      {text}
    </bdi>
  );
}
