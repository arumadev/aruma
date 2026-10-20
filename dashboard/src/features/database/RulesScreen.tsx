'use client';

import { useRef, useState, type FormEvent } from 'react';
import { Label, TextArea, TextField as AriaTextField } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { RuleError } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import styles from './RulesScreen.module.css';
import { LOCKED_RULES, offsetOf, openAccess, ruleErrors, TEST_MODE_RULES } from './rulesText';

/** The database's security rules: edited as text, checked by the server when saved. */
export function RulesScreen() {
  const t = useTranslations('database.rules');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const rules = useResource((signal) => api.getRules(project.projectId, signal), [project.projectId]);
  useDocumentTitle(t('title'));

  return (
    <Card title={t('title')} description={t('description')}>
      {rules.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={rules.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(rules.error)}
        </Banner>
      ) : !rules.data ? (
        <Skeleton lines={8} />
      ) : (
        <RulesEditor key={rules.data.rules} saved={rules.data.rules} onSaved={(doc) => rules.setData({ rules: doc })} />
      )}
    </Card>
  );
}

function RulesEditor({ saved, onSaved }: { saved: string; onSaved: (doc: string) => void }) {
  const t = useTranslations('database.rules');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const [text, setText] = useState(saved);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // The errors of the last refused save, kept until the text changes.
  const [errors, setErrors] = useState<{ text: string; list: RuleError[] } | null>(null);
  const area = useRef<HTMLTextAreaElement>(null);
  const gutter = useRef<HTMLDivElement>(null);
  const changed = text !== saved;
  const open = openAccess(text);
  const shown = errors?.text === text ? errors.list : [];
  const errorLines = new Set(shown.map((e) => e.line));
  const lineCount = text.split('\n').length;

  async function save(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      const result = await api.saveRules(project.projectId, text);
      setErrors(null);
      notify(t('published'));
      onSaved(result.rules);
    } catch (err) {
      const list = ruleErrors(err);
      if (list.length > 0) {
        setErrors({ text, list });
      } else {
        setError(err);
      }
      setPending(false);
    }
  }

  // Puts the caret where an error is and scrolls it into view.
  function goTo(e: RuleError) {
    const el = area.current;
    if (!el) return;
    const at = offsetOf(text, e.line, e.column);
    el.focus();
    el.setSelectionRange(at, at);
  }

  return (
    <form className={styles.form} onSubmit={save} noValidate>
      <div className={styles.templates}>
        <span className={styles.templatesLabel}>{t('templates')}</span>
        <Button variant="outlined" icon="lock" isDisabled={!editable} onPress={() => setText(LOCKED_RULES)}>
          {t('locked')}
        </Button>
        <Button variant="outlined" icon="warning" isDisabled={!editable} onPress={() => setText(TEST_MODE_RULES)}>
          {t('testMode')}
        </Button>
      </div>
      {(open.read || open.write) && (
        <Banner tone="warning" title={t('openTitle')}>
          {open.write ? t('openWrite') : t('openRead')}
        </Banner>
      )}
      {shown.length > 0 && (
        <Banner tone="error" title={t('invalidTitle', { count: shown.length })}>
          <ul className={styles.errors}>
            {shown.map((e, i) => (
              <li key={i}>
                <button type="button" className={styles.errorLink} onClick={() => goTo(e)}>
                  {t('at', { line: e.line, column: e.column })}
                </button>{' '}
                <bdi dir="ltr" className={styles.message}>
                  {e.message}
                  {e.path && ` (${e.path})`}
                </bdi>
              </li>
            ))}
          </ul>
        </Banner>
      )}
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      <AriaTextField value={text} onChange={setText} isReadOnly={!editable} isInvalid={shown.length > 0} className={styles.field}>
        <Label className={styles.label}>{t('editor')}</Label>
        <div className={styles.editor} dir="ltr">
          <div className={styles.gutter} ref={gutter} aria-hidden="true">
            {Array.from({ length: lineCount }, (_, i) => (
              <span key={i} className={errorLines.has(i + 1) ? styles.errorLine : undefined}>
                {i + 1}
              </span>
            ))}
          </div>
          <TextArea
            ref={area}
            className={styles.area}
            rows={Math.min(Math.max(lineCount, 12), 30)}
            spellCheck="false"
            autoCapitalize="off"
            autoCorrect="off"
            wrap="off"
            onScroll={(e) => {
              if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
            }}
          />
        </div>
      </AriaTextField>
      {editable && (
        <div className={styles.actions}>
          <Button type="submit" isPending={pending} isDisabled={!changed}>
            {t('publish')}
          </Button>
          <Button variant="text" icon="undo" isDisabled={!changed || pending} onPress={() => setText(saved)}>
            {t('revert')}
          </Button>
        </div>
      )}
    </form>
  );
}
