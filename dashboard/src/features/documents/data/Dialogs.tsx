'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Dialog } from '@/components/ui/Dialog';
import { TextField } from '@/components/ui/TextField';
import { ApiError } from '@/lib/api/client';
import { useErrorText } from '@/lib/errors';
import * as api from '../api';
import { FieldRow, MapEntries, newEntry } from './ValueEditor';
import {
  autoId,
  displayPath,
  draftOf,
  emptyDraft,
  fieldPath,
  idProblem,
  isValid,
  nestedFields,
  newKey,
  valueAt,
  valueOf,
  type Draft,
  type DraftEntry,
} from './values';
import styles from './Dialogs.module.css';

/** The text of a failed write: the conflicts and duplicates of the data browser, else the usual. */
export function useWriteErrorText() {
  const t = useTranslations('documents.data');
  const errorText = useErrorText();
  return (error: unknown): string => {
    if (error instanceof ApiError) {
      if (error.status === 'ALREADY_EXISTS') return t('exists');
      if (error.status === 'FAILED_PRECONDITION') return t('conflict');
      // The service's own texts, production's, name what is wrong with a value or a path.
      if (error.status === 'INVALID_ARGUMENT' && error.fieldViolations.length === 0 && error.message) return error.message;
    }
    return errorText.message(error);
  };
}

const isConflict = (error: unknown) => error instanceof ApiError && error.status === 'FAILED_PRECONDITION';

/** Runs a write for a dialog: its progress, and its failure shown in the dialog. */
function useWrite() {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  async function run(write: () => Promise<void>) {
    setPending(true);
    setError(null);
    try {
      await write();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }
  return { pending, error, run };
}

function WriteError({ error, onReload }: { error: unknown; onReload?: () => void }) {
  const t = useTranslations('documents.data');
  const text = useWriteErrorText();
  if (!error) return null;
  return (
    <Banner
      tone="error"
      action={
        isConflict(error) && onReload ? (
          <Button variant="text" onPress={onReload}>
            {t('reload')}
          </Button>
        ) : undefined
      }
    >
      {text(error)}
    </Banner>
  );
}

/** A read-only path line of a dialog: "Parent path  /users". */
function PathLine({ label, path }: { label: string; path: string }) {
  return (
    <div className={styles.pathLine}>
      <span className={styles.pathLabel}>{label}</span>
      <Ltr>{`/${path}`}</Ltr>
    </div>
  );
}

const fieldsOf = (entries: DraftEntry[], database: string): api.Fields =>
  Object.fromEntries(entries.map((e) => [e.name, valueOf(e.value, database)]));

function entriesValid(entries: DraftEntry[], database: string): boolean {
  return isValid({ ...emptyDraft('map'), entries }, database);
}

/** A new document's id (or Auto-ID) and its fields. */
function NewDocumentFields({
  parentPath,
  id,
  onId,
  entries,
  onEntries,
  database,
  autoFocus,
}: {
  parentPath: string;
  id: string;
  onId: (id: string) => void;
  entries: DraftEntry[];
  onEntries: (entries: DraftEntry[]) => void;
  database: string;
  autoFocus?: boolean;
}) {
  const t = useTranslations('documents.data.editor');
  const tp = useTranslations('documents.data.problem');
  const [touched, setTouched] = useState(false);
  const problem = idProblem(id);
  return (
    <>
      <PathLine label={t('documentParentPath')} path={parentPath} />
      <div className={styles.idLine}>
        <TextField
          label={t('documentId')}
          value={id}
          onChange={onId}
          onBlur={() => setTouched(true)}
          ltr
          autoFocus={autoFocus}
          errorMessage={touched && problem ? tp(problem) : undefined}
        />
        <Button variant="text" onPress={() => onId(autoId())}>
          {t('autoId')}
        </Button>
      </div>
      <MapEntries entries={entries} database={database} onChange={onEntries} label={t('fieldsGroup')} />
    </>
  );
}

/** "Add a document": an id (or Auto-ID) and fields, created only if no document has that id. */
export function AddDocumentDialog({
  projectId,
  database,
  collectionPath,
  onClose,
  onCreated,
}: {
  projectId: string;
  database: string;
  collectionPath: string;
  onClose: () => void;
  onCreated: (doc: api.Doc) => void;
}) {
  const t = useTranslations('documents.data.editor');
  const tCommon = useTranslations('common');
  const [id, setId] = useState('');
  const [entries, setEntries] = useState<DraftEntry[]>(() => [newEntry()]);
  const write = useWrite();
  const valid = idProblem(id) === null && entriesValid(entries, database);
  return (
    <Dialog
      isOpen
      wide
      onOpenChange={(open) => !open && onClose()}
      title={t('addTitle')}
      isBusy={write.pending}
      onSubmit={() =>
        valid &&
        void write.run(async () => {
          onCreated(await api.createDocument(projectId, collectionPath, { documentId: id, fields: fieldsOf(entries, database) }));
        })
      }
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={write.pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" isDisabled={!valid} isPending={write.pending}>
            {t('save')}
          </Button>
        </>
      }
    >
      <NewDocumentFields parentPath={collectionPath} id={id} onId={setId} entries={entries} onEntries={setEntries} database={database} autoFocus />
      <WriteError error={write.error} />
    </Dialog>
  );
}

/** "Start a collection": its id, then its first document, which it must have to exist. */
export function StartCollectionDialog({
  projectId,
  database,
  parentPath,
  onClose,
  onCreated,
}: {
  projectId: string;
  database: string;
  /** "" for the root, else a document. */
  parentPath: string;
  onClose: () => void;
  onCreated: (doc: api.Doc) => void;
}) {
  const t = useTranslations('documents.data.editor');
  const tp = useTranslations('documents.data.problem');
  const tCommon = useTranslations('common');
  const [step, setStep] = useState<1 | 2>(1);
  const [collection, setCollection] = useState('');
  const [touched, setTouched] = useState(false);
  const [id, setId] = useState('');
  const [entries, setEntries] = useState<DraftEntry[]>(() => [newEntry()]);
  const write = useWrite();
  const collectionProblem = idProblem(collection);
  const collectionPath = parentPath ? `${parentPath}/${collection}` : collection;
  const valid = idProblem(id) === null && entriesValid(entries, database);

  return (
    <Dialog
      isOpen
      wide
      onOpenChange={(open) => !open && onClose()}
      title={t('startTitle')}
      isBusy={write.pending}
      onSubmit={() => {
        if (step === 1) {
          setTouched(true);
          if (!collectionProblem) setStep(2);
          return;
        }
        if (valid)
          void write.run(async () => {
            onCreated(await api.createDocument(projectId, collectionPath, { documentId: id, fields: fieldsOf(entries, database) }));
          });
      }}
      actions={
        step === 1 ? (
          <>
            <Button variant="text" onPress={onClose}>
              {tCommon('cancel')}
            </Button>
            <Button type="submit" isDisabled={collection === ''}>
              {t('next')}
            </Button>
          </>
        ) : (
          <>
            <Button variant="text" onPress={() => setStep(1)} isDisabled={write.pending}>
              {t('back')}
            </Button>
            <Button variant="text" onPress={onClose} isDisabled={write.pending}>
              {tCommon('cancel')}
            </Button>
            <Button type="submit" isDisabled={!valid} isPending={write.pending}>
              {t('save')}
            </Button>
          </>
        )
      }
    >
      <ol className={styles.steps}>
        <li aria-current={step === 1 ? 'step' : undefined}>{t('stepCollection')}</li>
        <li aria-current={step === 2 ? 'step' : undefined}>{t('stepDocument')}</li>
      </ol>
      {step === 1 ? (
        <>
          <PathLine label={t('parentPath')} path={parentPath} />
          <TextField
            label={t('collectionId')}
            value={collection}
            onChange={setCollection}
            onBlur={() => setTouched(true)}
            ltr
            autoFocus
            errorMessage={touched && collectionProblem ? tp(collectionProblem) : undefined}
          />
        </>
      ) : (
        <NewDocumentFields parentPath={collectionPath} id={id} onId={setId} entries={entries} onEntries={setEntries} database={database} autoFocus />
      )}
      <WriteError error={write.error} />
    </Dialog>
  );
}

/**
 * Adds a field to a document, or edits the field at a path of map keys. The write sets that field
 * alone (its update mask), and only over the version the editor read (its updateTime), so an edit
 * made over another's fails and says so.
 */
export function FieldDialog({
  projectId,
  database,
  doc,
  path,
  onClose,
  onSaved,
  onReload,
}: {
  projectId: string;
  database: string;
  doc: api.Doc;
  /** The field edited, as map keys; absent, a new top-level field. */
  path?: string[];
  onClose: () => void;
  onSaved: (doc: api.Doc, name: string) => void;
  onReload: () => void;
}) {
  const t = useTranslations('documents.data.editor');
  const tData = useTranslations('documents.data');
  const tCommon = useTranslations('common');
  const [entry, setEntry] = useState<DraftEntry>(() => {
    if (!path) return newEntry();
    const v = valueAt(doc.fields, path);
    return { key: newKey(), name: path.at(-1) ?? '', value: v ? draftOf(v, database) : emptyDraft('string') };
  });
  const write = useWrite();
  // A new field may not take an existing one's name.
  const taken = path ? [] : Object.keys(doc.fields ?? {});
  const valid = entry.name !== '' && !taken.includes(entry.name) && isValid(entry.value, database);

  function save() {
    if (!valid) return;
    const segments = path ?? [entry.name];
    const value: Draft = entry.value;
    void write.run(async () => {
      const saved = await api.updateDocument(projectId, doc.path, {
        fields: nestedFields(segments, valueOf(value, database)),
        updateMask: [fieldPath(segments)],
        ...(doc.updateTime ? { updateTime: doc.updateTime } : { exists: true }),
      });
      onSaved(saved, displayPath(segments));
    });
  }

  return (
    <Dialog
      isOpen
      wide
      onOpenChange={(open) => !open && onClose()}
      title={path ? t('editFieldTitle') : t('addFieldTitle')}
      isBusy={write.pending}
      onSubmit={save}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={write.pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" isDisabled={!valid} isPending={write.pending}>
            {path ? t('update') : t('add')}
          </Button>
        </>
      }
    >
      <PathLine label={tData('documentLocation')} path={doc.path} />
      <FieldRow entry={entry} siblings={taken} database={database} onChange={setEntry} fixedName={Boolean(path)} autoFocus={!path} />
      <WriteError
        error={write.error}
        onReload={() => {
          onReload();
          onClose();
        }}
      />
    </Dialog>
  );
}

/** Deletes a field: the update mask names it and the fields leave it out. */
export function deleteField(projectId: string, doc: api.Doc, path: string[]) {
  return api.updateDocument(projectId, doc.path, { fields: {}, updateMask: [fieldPath(path)], exists: true });
}

/** "Delete Data" for a field, which a shift-click skips. */
export function DeleteFieldDialog({ doc, path, onConfirm, onClose }: { doc: api.Doc; path: string[]; onConfirm: () => Promise<void>; onClose: () => void }) {
  const t = useTranslations('documents.data');
  return (
    <ConfirmDialog title={t('deleteFieldTitle')} confirmLabel={t('delete')} danger onConfirm={onConfirm} onClose={onClose}>
      <Banner tone="warning">{t('deleteFieldBanner', { field: displayPath(path) })}</Banner>
      <PathLine label={t('documentLocation')} path={doc.path} />
      <div className={styles.pathLine}>
        <span className={styles.pathLabel}>{t('fieldPath')}</span>
        <Ltr>{displayPath(path)}</Ltr>
      </div>
      <p className={styles.hint}>{t('shiftHint')}</p>
    </ConfirmDialog>
  );
}

/** Deletes a document or a collection and everything under it, a bounded step at a time, until done. */
export async function recursiveDelete(projectId: string, path: string, onProgress: (deleted: number) => void): Promise<number> {
  let deleted = 0;
  for (;;) {
    const step = await api.recursiveDeleteStep(projectId, path);
    deleted += step.deleted;
    onProgress(deleted);
    if (step.done) return deleted;
  }
}

function Progress({ count }: { count: number | null }) {
  const t = useTranslations('documents.data');
  if (count === null) return null;
  return (
    <p role="status" className={styles.hint}>
      {t('progress', { count })}
    </p>
  );
}

/** "Delete Data" for a document: it, its subcollections and everything in them ("Start delete"). */
export function DeleteDocumentDialog({ projectId, path, onDeleted, onClose }: { projectId: string; path: string; onDeleted: () => void; onClose: () => void }) {
  const t = useTranslations('documents.data');
  const [count, setCount] = useState<number | null>(null);
  return (
    <ConfirmDialog
      title={t('deleteDocTitle')}
      confirmLabel={t('startDelete')}
      danger
      onClose={onClose}
      onConfirm={async () => {
        setCount(0);
        await recursiveDelete(projectId, path, setCount);
        onDeleted();
      }}
    >
      <Banner tone="warning">{t('deleteDocBanner')}</Banner>
      <PathLine label={t('documentLocation')} path={path} />
      <Progress count={count} />
    </ConfirmDialog>
  );
}

/** Deletes every field of a document over the version read; its subcollections stay. */
export function DeleteFieldsDialog({ projectId, doc, onDeleted, onClose }: { projectId: string; doc: api.Doc; onDeleted: (doc: api.Doc) => void; onClose: () => void }) {
  const t = useTranslations('documents.data');
  return (
    <ConfirmDialog
      title={t('deleteFieldsTitle')}
      confirmLabel={t('delete')}
      danger
      onClose={onClose}
      onConfirm={async () => {
        onDeleted(await api.updateDocument(projectId, doc.path, { fields: {}, ...(doc.updateTime ? { updateTime: doc.updateTime } : { exists: true }) }));
      }}
    >
      <Banner tone="warning">{t('deleteFieldsBanner')}</Banner>
      <PathLine label={t('documentLocation')} path={doc.path} />
    </ConfirmDialog>
  );
}

/** "Delete this collection?": the collection's id typed to confirm, then everything in it deleted. */
export function DeleteCollectionDialog({ projectId, path, onDeleted, onClose }: { projectId: string; path: string; onDeleted: () => void; onClose: () => void }) {
  const t = useTranslations('documents.data');
  const tp = useTranslations('documents.data.problem');
  const id = path.split('/').at(-1) ?? path;
  const [typed, setTyped] = useState('');
  const [touched, setTouched] = useState(false);
  const [count, setCount] = useState<number | null>(null);
  return (
    <ConfirmDialog
      title={t('deleteCollTitle')}
      confirmLabel={t('delete')}
      danger
      canConfirm={typed === id}
      onClose={onClose}
      onConfirm={async () => {
        setCount(0);
        await recursiveDelete(projectId, path, setCount);
        onDeleted();
      }}
    >
      <Banner tone="warning">{t('deleteCollBanner')}</Banner>
      <PathLine label={t('collectionPath')} path={path} />
      <p>{t('typeToConfirm', { id })}</p>
      <TextField
        label={t('confirmId')}
        value={typed}
        onChange={setTyped}
        onBlur={() => setTouched(true)}
        placeholder={id}
        ltr
        autoFocus
        errorMessage={touched && typed === '' ? tp('required') : undefined}
      />
      <Progress count={count} />
    </ConfirmDialog>
  );
}
