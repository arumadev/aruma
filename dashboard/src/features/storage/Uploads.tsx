'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Button, IconButton } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { ApiError } from '@/lib/api/client';
import { useErrorText } from '@/lib/errors';
import * as api from './api';
import { useBytes } from './Bytes';
import { resumableUpload, UploadError } from './upload';
import styles from './Uploads.module.css';

export type UploadState = 'queued' | 'uploading' | 'retrying' | 'done' | 'failed' | 'cancelled';

export interface Upload {
  id: number;
  name: string;
  file: File;
  sent: number;
  state: UploadState;
  error?: unknown;
}

/**
 * The page's uploads, one at a time in the order they were added. `onDone` runs after each
 * file is stored. Leaving the page while one runs asks first: the session dies with the tab.
 */
export function useUploads(projectId: string, onDone: (name: string) => void) {
  const [uploads, setUploads] = useState<Upload[]>([]);
  const controllers = useRef(new Map<number, AbortController>());
  const nextId = useRef(1);
  const done = useRef(onDone);
  useEffect(() => {
    done.current = onDone;
  });

  const update = useCallback((id: number, patch: Partial<Upload>) => {
    setUploads((list) => list.map((u) => (u.id === id ? { ...u, ...patch } : u)));
  }, []);

  const add = useCallback((files: { name: string; file: File }[]) => {
    setUploads((list) => [...list, ...files.map(({ name, file }) => ({ id: nextId.current++, name, file, sent: 0, state: 'queued' as const }))]);
  }, []);

  const running = uploads.find((u) => u.state === 'uploading' || u.state === 'retrying');
  const next = running ? undefined : uploads.find((u) => u.state === 'queued');

  const started = useRef(new Set<number>());
  useEffect(() => {
    if (!next || started.current.has(next.id)) return;
    started.current.add(next.id);
    const controller = new AbortController();
    controllers.current.set(next.id, controller);
    update(next.id, { state: 'uploading', sent: 0, error: undefined });
    resumableUpload({
      name: next.name,
      file: next.file,
      contentType: next.file.type || undefined,
      signal: controller.signal,
      issue: (req, signal) => api.issueUpload(projectId, req, signal),
      onProgress: (sent) => update(next.id, { sent, state: 'uploading' }),
      onRetry: () => update(next.id, { state: 'retrying' }),
    }).then(
      () => {
        update(next.id, { state: 'done', sent: next.file.size });
        done.current(next.name);
      },
      (error: unknown) => {
        const cancelled = error instanceof UploadError && error.kind === 'cancelled';
        update(next.id, cancelled ? { state: 'cancelled' } : { state: 'failed', error });
      },
    ).finally(() => controllers.current.delete(next.id));
  }, [next, projectId, update]);

  // Nothing outlives the page: leaving it cancels what runs, and asks first.
  const active = uploads.some((u) => u.state === 'queued' || u.state === 'uploading' || u.state === 'retrying');
  useEffect(() => {
    if (!active) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [active]);
  useEffect(() => {
    const live = controllers.current;
    return () => {
      for (const c of live.values()) c.abort();
    };
  }, []);

  const cancel = useCallback(
    (id: number) => {
      const c = controllers.current.get(id);
      if (c) c.abort();
      else update(id, { state: 'cancelled' });
    },
    [update],
  );
  // A retry is a new upload, with a new session, in the same place in the list.
  const retry = useCallback((id: number) => update(id, { id: nextId.current++, state: 'queued', sent: 0, error: undefined }), [update]);
  const clearFinished = useCallback(() => setUploads((list) => list.filter((u) => u.state !== 'done' && u.state !== 'cancelled')), []);

  return { uploads, add, cancel, retry, clearFinished };
}

/** The uploads of this visit, with their progress and what can be done with each. */
export function UploadList({ uploads, onCancel, onRetry, onClear }: { uploads: Upload[]; onCancel: (id: number) => void; onRetry: (id: number) => void; onClear: () => void }) {
  const t = useTranslations('storage.upload');
  const bytes = useBytes();
  const errorText = useErrorText();
  if (uploads.length === 0) return null;

  const failure = (error: unknown) => {
    if (error instanceof ApiError) return errorText.message(error);
    if (!(error instanceof UploadError)) return t('error.failed');
    if (error.kind === 'expired') return t('error.expired');
    if (error.kind === 'failed') return t('error.failed');
    switch (error.status) {
      case 402:
        return t('error.quota');
      case 403:
        return t('error.denied');
      case 413:
        return t('error.tooLarge');
      default:
        return t('error.refused', { status: error.status });
    }
  };

  return (
    <section className={styles.panel} aria-label={t('title')}>
      <header className={styles.header}>
        <h2 className={styles.title}>{t('title')}</h2>
        {uploads.some((u) => u.state === 'done' || u.state === 'cancelled') && (
          <Button variant="text" onPress={onClear}>
            {t('clear')}
          </Button>
        )}
      </header>
      <ul className={styles.list}>
        {uploads.map((u) => {
          const size = u.file.size;
          return (
            <li key={u.id} className={styles.item}>
              <div className={styles.row}>
                <span className={styles.name}>
                  <Ltr>{u.name}</Ltr>
                </span>
                <span className={styles.status} role={u.state === 'failed' ? 'alert' : undefined}>
                  {u.state === 'failed'
                    ? failure(u.error)
                    : t(`state.${u.state}`, { sent: bytes(u.sent), total: bytes(size) })}
                </span>
                {(u.state === 'queued' || u.state === 'uploading' || u.state === 'retrying') && (
                  <IconButton icon="close" label={t('cancel', { name: u.name })} onPress={() => onCancel(u.id)} />
                )}
                {(u.state === 'failed' || u.state === 'cancelled') && (
                  <IconButton icon="rotate" label={t('retry', { name: u.name })} onPress={() => onRetry(u.id)} />
                )}
              </div>
              {(u.state === 'uploading' || u.state === 'retrying' || u.state === 'queued') && (
                <progress className={styles.progress} max={Math.max(size, 1)} value={u.sent} aria-label={t('progress', { name: u.name })} />
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
