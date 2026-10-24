'use client';

import { useRef, useState, type FormEvent } from 'react';
import { Label, TextArea, TextField as AriaTextField } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import * as api from './api';
import styles from './RulesScreen.module.css';
import { issueRange, LOCKED_RULES, openAccess, PER_USER_RULES, refusedIssues, SIGNED_IN_RULES, sortIssues } from './rulesText';

/**
 * The bucket's security rules, edited as text. The server compiles them on Check and on
 * Publish, and its issues show on their lines; a source with an error is never published.
 */
export function RulesScreen() {
  const t = useTranslations('storage.rules');
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
        <RulesEditor key={`${rules.data.published}:${rules.data.source}`} saved={rules.data} onSaved={rules.setData} />
      )}
    </Card>
  );
}

type Shown = { text: string; issues: api.RuleIssue[]; checked: boolean };

function RulesEditor({ saved, onSaved }: { saved: api.PublishedRules; onSaved: (rules: api.PublishedRules) => void }) {
  const t = useTranslations('storage.rules');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const initial = saved.published ? saved.source : LOCKED_RULES;
  const [text, setText] = useState(initial);
  const [pending, setPending] = useState<'check' | 'publish' | null>(null);
  const [error, setError] = useState<unknown>(null);
  // The issues of the last check or publish, kept until the text changes.
  const [shown, setShown] = useState<Shown | null>(saved.issues?.length ? { text: initial, issues: saved.issues, checked: false } : null);
  const area = useRef<HTMLTextAreaElement>(null);
  const gutter = useRef<HTMLDivElement>(null);
  const changed = text !== saved.source || !saved.published;
  const current = shown?.text === text ? sortIssues(shown.issues) : [];
  const errors = current.filter((i) => i.severity === 'ERROR');
  const lines = new Map<number, 'error' | 'warning'>();
  for (const i of current) {
    const line = i.sourcePosition?.line;
    if (line && lines.get(line) !== 'error') lines.set(line, i.severity === 'ERROR' ? 'error' : 'warning');
  }
  const lineCount = text.split('\n').length;

  async function check() {
    setPending('check');
    setError(null);
    try {
      const result = await api.validateRules(project.projectId, text);
      setShown({ text, issues: result.issues, checked: true });
      if (result.valid && result.issues.length === 0) notify(t('valid'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(null);
    }
  }

  async function publish(event: FormEvent) {
    event.preventDefault();
    setPending('publish');
    setError(null);
    try {
      const result = await api.publishRules(project.projectId, text);
      notify(t('published'));
      onSaved(result);
    } catch (err) {
      const issues = refusedIssues(err);
      if (issues.length > 0) setShown({ text, issues, checked: false });
      else setError(err);
      setPending(null);
    }
  }

  // Selects what an issue marks, and scrolls it into view.
  function goTo(issue: api.RuleIssue) {
    const el = area.current;
    const range = issueRange(text, issue);
    if (!el || !range) return;
    el.focus();
    el.setSelectionRange(range[0], range[1]);
  }

  return (
    <form className={styles.form} onSubmit={publish} noValidate>
      {!saved.published ? (
        <Banner tone="info" title={t('defaultTitle')}>
          {t('defaultBody')}
        </Banner>
      ) : (
        saved.updateTime && (
          <p className={styles.meta}>
            {t.rich('publishedAt', { time: () => <Time value={saved.updateTime!} /> })}
          </p>
        )
      )}
      <div className={styles.templates}>
        <span className={styles.templatesLabel}>{t('templates')}</span>
        <Button variant="outlined" icon="lock" isDisabled={!editable} onPress={() => setText(LOCKED_RULES)}>
          {t('locked')}
        </Button>
        <Button variant="outlined" icon="person" isDisabled={!editable} onPress={() => setText(PER_USER_RULES)}>
          {t('perUser')}
        </Button>
        <Button variant="outlined" icon="warning" isDisabled={!editable} onPress={() => setText(SIGNED_IN_RULES)}>
          {t('signedIn')}
        </Button>
      </div>
      {openAccess(text) && (
        <Banner tone="warning" title={t('openTitle')}>
          {t('openBody')}
        </Banner>
      )}
      {current.length > 0 && (
        <Banner tone={errors.length > 0 ? 'error' : 'warning'} title={errors.length > 0 ? t('invalidTitle', { count: errors.length }) : t('warningsTitle', { count: current.length })}>
          <ul className={styles.issues}>
            {current.map((issue, i) => (
              <li key={i}>
                <Badge tone={issue.severity === 'ERROR' ? 'error' : 'warning'}>{issue.severity === 'ERROR' ? t('error') : t('warning')}</Badge>{' '}
                {issue.sourcePosition ? (
                  <button type="button" className={styles.issueLink} onClick={() => goTo(issue)}>
                    {t('at', { line: issue.sourcePosition.line, column: issue.sourcePosition.column })}
                  </button>
                ) : null}{' '}
                <bdi dir="ltr" className={styles.message}>
                  {issue.description}
                </bdi>
              </li>
            ))}
          </ul>
        </Banner>
      )}
      {shown?.checked && shown.text === text && current.length === 0 && <Banner tone="info">{t('validBody')}</Banner>}
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      <AriaTextField value={text} onChange={setText} isReadOnly={!editable} isInvalid={errors.length > 0} className={styles.field}>
        <Label className={styles.label}>{t('editor')}</Label>
        <div className={styles.editor} dir="ltr">
          <div className={styles.gutter} ref={gutter} aria-hidden="true">
            {Array.from({ length: lineCount }, (_, i) => (
              <span
                key={i}
                className={lines.get(i + 1) === 'error' ? styles.errorLine : lines.get(i + 1) === 'warning' ? styles.warningLine : undefined}
                data-line={i + 1}
                data-issue={lines.get(i + 1)}
              >
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
          <Button type="submit" isPending={pending === 'publish'} isDisabled={!changed || pending !== null}>
            {t('publish')}
          </Button>
          <Button variant="outlined" icon="check" isPending={pending === 'check'} isDisabled={pending !== null} onPress={() => void check()}>
            {t('check')}
          </Button>
          <Button variant="text" icon="undo" isDisabled={text === initial || pending !== null} onPress={() => setText(initial)}>
            {t('revert')}
          </Button>
        </div>
      )}
    </form>
  );
}
