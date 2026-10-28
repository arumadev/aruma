'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Button, IconButton } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { TextField } from '@/components/ui/TextField';
import {
  edited,
  emptyDraft,
  FIELD_TYPES,
  nameProblem,
  newKey,
  retyped,
  valueProblem,
  type Draft,
  type DraftEntry,
  type FieldType,
  type ValueProblem,
} from './values';
import styles from './ValueEditor.module.css';

/** A new field row of a map: no name yet, a string. */
export const newEntry = (): DraftEntry => ({ key: newKey(), name: '', value: emptyDraft('string') });

/** Shows a problem once its control was left, as the Firebase console checks on blur. */
function useTouched(): [boolean, () => void] {
  const [touched, setTouched] = useState(false);
  return [touched, () => setTouched(true)];
}

function useProblemText() {
  const t = useTranslations('documents.data.problem');
  return (p: ValueProblem | null) => (p ? t(p) : undefined);
}

/** A field's type, as a select of the ten types (bytes, read-only, only for a value that has them). */
export function TypeSelect({ value, onChange, label }: { value: Draft['type']; onChange: (type: FieldType) => void; label: string }) {
  const options: { id: string; label: string }[] = FIELD_TYPES.map((type) => ({ id: type, label: type }));
  if (value === 'bytes') options.push({ id: 'bytes', label: 'bytes' });
  return <Select label={label} options={options} value={value} onChange={(id) => onChange(id as FieldType)} className={styles.type} isDisabled={value === 'bytes'} />;
}

/**
 * A field of a map as edited: its name, its type and its value. The name is fixed when it names a
 * field being edited in place.
 */
export function FieldRow({
  entry,
  siblings,
  database,
  onChange,
  onRemove,
  fixedName,
  autoFocus,
}: {
  entry: DraftEntry;
  /** The names of the fields before this one, which it may not repeat. */
  siblings: readonly string[];
  database: string;
  onChange: (entry: DraftEntry) => void;
  onRemove?: () => void;
  fixedName?: boolean;
  autoFocus?: boolean;
}) {
  const t = useTranslations('documents.data.editor');
  const problemText = useProblemText();
  const [touched, touch] = useTouched();
  return (
    <div role="group" aria-label={entry.name || t('field')} className={styles.row}>
      <div className={styles.line}>
        <TextField
          label={t('field')}
          value={entry.name}
          onChange={(name) => onChange({ ...entry, name })}
          onBlur={touch}
          isReadOnly={fixedName}
          autoFocus={autoFocus}
          className={styles.name}
          errorMessage={touched ? problemText(nameProblem(entry.name, siblings)) : undefined}
        />
        <TypeSelect label={t('type')} value={entry.value.type} onChange={(type) => onChange({ ...entry, value: retyped(entry.value, type) })} />
        {onRemove && <IconButton icon="close" label={t('removeField', { name: entry.name })} onPress={onRemove} />}
      </div>
      <ValueEditor draft={entry.value} name={entry.name} database={database} onChange={(value) => onChange({ ...entry, value })} />
    </div>
  );
}

/** A map's fields as edited, with a button that adds one. */
export function MapEntries({
  entries,
  database,
  onChange,
  label,
}: {
  entries: DraftEntry[];
  database: string;
  onChange: (entries: DraftEntry[]) => void;
  label: string;
}) {
  const t = useTranslations('documents.data');
  return (
    <div role="group" aria-label={label} className={styles.entries}>
      {entries.map((e, i) => (
        <FieldRow
          key={e.key}
          entry={e}
          siblings={entries.slice(0, i).map((x) => x.name)}
          database={database}
          onChange={(next) => onChange(entries.map((x) => (x.key === e.key ? next : x)))}
          onRemove={() => onChange(entries.filter((x) => x.key !== e.key))}
        />
      ))}
      <div>
        <Button variant="text" icon="add" onPress={() => onChange([...entries, newEntry()])}>
          {t('addField')}
        </Button>
      </div>
    </div>
  );
}

/** The value part of a field's editor, by its type; a map or an array nests its own fields. */
export function ValueEditor({ draft, name, database, onChange }: { draft: Draft; name: string; database: string; onChange: (d: Draft) => void }) {
  const t = useTranslations('documents.data.editor');
  const problemText = useProblemText();
  const [touched, touch] = useTouched();
  const problem = touched ? problemText(valueProblem(draft, database)) : undefined;
  const set = (patch: Partial<Draft>) => onChange(edited(draft, patch));

  switch (draft.type) {
    case 'string':
      return <TextField label={t('value')} value={draft.text} onChange={(text) => set({ text })} multiline rows={1} className={styles.value} />;
    case 'int64':
    case 'double':
    case 'reference':
      return (
        <TextField
          label={t('value')}
          value={draft.text}
          onChange={(text) => set({ text })}
          onBlur={touch}
          ltr
          placeholder={draft.type === 'reference' ? 'users/alice' : undefined}
          className={styles.value}
          errorMessage={problem}
        />
      );
    case 'boolean':
      return (
        <Select
          label={t('value')}
          options={[
            { id: 'true', label: 'true' },
            { id: 'false', label: 'false' },
          ]}
          value={String(draft.bool)}
          onChange={(v) => set({ bool: v === 'true' })}
          className={styles.value}
        />
      );
    case 'null':
      return null;
    case 'bytes':
      return (
        <TextField label={t('value')} value={draft.text} isReadOnly ltr description={t('bytes')} className={styles.value} />
      );
    case 'timestamp':
      return (
        <div className={styles.line}>
          <TextField label={t('date')} type="date" value={draft.date} onChange={(date) => set({ date })} onBlur={touch} ltr errorMessage={problem} />
          <TextField label={t('time')} type="time" value={draft.time} onChange={(time) => set({ time })} onBlur={touch} ltr />
        </div>
      );
    case 'geopoint': {
      const p = valueProblem(draft, database);
      return (
        <div className={styles.line}>
          <TextField
            label={t('latitude')}
            value={draft.lat}
            onChange={(lat) => set({ lat })}
            onBlur={touch}
            ltr
            inputMode="decimal"
            errorMessage={touched && p === 'latitude' ? problemText(p) : undefined}
          />
          <TextField
            label={t('longitude')}
            value={draft.lng}
            onChange={(lng) => set({ lng })}
            onBlur={touch}
            ltr
            inputMode="decimal"
            errorMessage={touched && p === 'longitude' ? problemText(p) : undefined}
          />
        </div>
      );
    }
    case 'map':
      return (
        <div className={styles.nested}>
          <MapEntries entries={draft.entries} database={database} onChange={(entries) => set({ entries })} label={t('mapGroup', { name })} />
        </div>
      );
    case 'array':
      return (
        <div className={styles.nested}>
          <ArrayItems draft={draft} name={name} database={database} onChange={(entries) => set({ entries })} />
        </div>
      );
  }
}

function ArrayItems({ draft, name, database, onChange }: { draft: Draft; name: string; database: string; onChange: (entries: DraftEntry[]) => void }) {
  const t = useTranslations('documents.data.editor');
  const entries = draft.entries;
  return (
    <div role="group" aria-label={t('arrayGroup', { name })} className={styles.entries}>
      {entries.map((e, i) => {
        const label = t('item', { index: i });
        const update = (value: Draft) => onChange(entries.map((x) => (x.key === e.key ? { ...x, value } : x)));
        return (
          <div key={e.key} role="group" aria-label={label} className={styles.row}>
            <div className={styles.line}>
              <span className={styles.index} dir="ltr">
                {i}
              </span>
              <TypeSelect label={t('type')} value={e.value.type} onChange={(type) => update(retyped(e.value, type))} />
              <IconButton icon="close" label={t('removeItem', { index: i })} onPress={() => onChange(entries.filter((x) => x.key !== e.key))} />
            </div>
            <ValueEditor draft={e.value} name={label} database={database} onChange={update} />
          </div>
        );
      })}
      <div>
        <Button variant="text" icon="add" onPress={() => onChange([...entries, { key: newKey(), name: '', value: emptyDraft('string') }])}>
          {t('addItem')}
        </Button>
      </div>
    </div>
  );
}
