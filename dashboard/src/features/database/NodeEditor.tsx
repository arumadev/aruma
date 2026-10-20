'use client';

import { useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { TextField } from '@/components/ui/TextField';
import * as api from '@/lib/api/console';
import type { DatabaseLimits, JsonValue } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import styles from './NodeEditor.module.css';
import { checkKey, checkValue, childPath, parseValueInput, pathKeys, type ValueCheck, type ValueProblem } from './validation';

export interface NodeEditorProps {
  projectId: string;
  /** `edit` replaces the value at `path`; `add` writes a new child under it. */
  mode: 'edit' | 'add';
  path: string;
  /** The value field's starting text, for `edit`. */
  initial?: string;
  limits: DatabaseLimits;
  /** Called with the path written once the server has it. */
  onSaved: (path: string) => void;
  onCancel: () => void;
}

/**
 * Edits a leaf in place or adds a child. The key and the value are checked as the server and
 * the SDKs check them before anything is sent; the server stays the authority.
 */
export function NodeEditor({ projectId, mode, path, initial = '', limits, onSaved, onCancel }: NodeEditorProps) {
  const t = useTranslations('database.editor');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const [key, setKey] = useState('');
  const [text, setText] = useState(initial);
  const [submitted, setSubmitted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const target = mode === 'add' ? childPath(path, key) : path;
  const keyProblem = mode === 'add' ? checkKey(key, limits) : null;
  const parsed = parseValueInput(text);
  const valueCheck: ValueCheck | null = parsed.problem === null ? checkValue(parsed.value, pathKeys(target), limits) : null;

  const keyMessage = keyProblem && t(`key.${keyProblem}`, { max: limits.keyBytes });
  let valueMessage: string | undefined;
  if (parsed.problem) valueMessage = t(`value.${parsed.problem}`);
  else if (valueCheck?.problem) {
    const max: Partial<Record<ValueProblem, number>> = { string: limits.stringBytes, depth: limits.depth, path: limits.pathBytes, size: limits.writeBytes };
    valueMessage = t(`value.${valueCheck.problem}`, { key: valueCheck.key ?? '', max: max[valueCheck.problem] ?? 0 });
  }
  const valid = !keyMessage && !valueMessage;

  async function save(event: FormEvent) {
    event.preventDefault();
    setSubmitted(true);
    if (!valid || parsed.problem !== null) return;
    setPending(true);
    setError(null);
    try {
      await api.setData(projectId, target, parsed.value as JsonValue);
      onSaved(target);
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  return (
    <form className={styles.editor} onSubmit={save} noValidate aria-label={mode === 'add' ? t('addLabel', { path }) : t('editLabel', { path })}>
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      <div className={styles.fields}>
        {mode === 'add' && (
          <TextField
            className={styles.key}
            label={t('keyLabel')}
            ltr
            autoFocus
            spellCheck="false"
            value={key}
            onChange={setKey}
            errorMessage={submitted || key !== '' ? (keyMessage ?? undefined) : undefined}
          />
        )}
        <TextField
          className={styles.value}
          label={t('valueLabel')}
          ltr
          autoFocus={mode === 'edit'}
          spellCheck="false"
          value={text}
          onChange={setText}
          errorMessage={submitted || text !== initial ? valueMessage : undefined}
          description={parsed.problem === null ? t(`savedAs.${kindOf(parsed.value)}`) : t('valueHint')}
        />
      </div>
      <div className={styles.actions}>
        <Button type="submit" isPending={pending}>
          {mode === 'add' ? t('add') : tCommon('save')}
        </Button>
        <Button variant="text" onPress={onCancel} isDisabled={pending}>
          {tCommon('cancel')}
        </Button>
      </div>
    </form>
  );
}

/** What a value is saved as, in the words the hint uses. */
function kindOf(value: JsonValue): 'text' | 'number' | 'boolean' | 'object' | 'null' {
  if (value === null) return 'null';
  if (typeof value === 'string') return 'text';
  if (typeof value === 'number') return 'number';
  if (typeof value === 'boolean') return 'boolean';
  return 'object';
}
