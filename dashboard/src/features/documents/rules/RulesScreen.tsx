'use client';

import { useEffect, useRef, useState } from 'react';
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
import * as api from '../api';
import { Playground, SimulationBanner, usePlayground } from './Playground';
import styles from './RulesScreen.module.css';
import { issueRange, LOCKED_RULES, lineMarks, refusedIssues, rulesetId, sortIssues } from './rulesText';

/** How long the text rests before the server checks it, so typing is not a call a key. */
export const VALIDATE_DELAY_MS = 350;

/**
 * The database's security rules: the versions published so far, an editor whose errors the
 * server marks as you type, Publish and Discard for what you changed, and the Rules Playground.
 */
export function RulesScreen() {
  const t = useTranslations('documents.rules');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const rules = useResource((signal) => api.getRules(project.projectId, signal), [project.projectId]);
  const history = useResource((signal) => api.listRulesets(project.projectId, { pageSize: 30 }, signal), [project.projectId]);
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
        <RulesEditor
          key={`${rules.data.published}:${rules.data.rulesetName ?? ''}:${rules.data.source}`}
          saved={rules.data}
          versions={history.data?.rulesets ?? []}
          onPublished={(result) => {
            rules.setData(result);
            history.reload();
          }}
        />
      )}
    </Card>
  );
}

type Checked = { text: string; issues: api.RuleIssue[] };

function RulesEditor({ saved, versions, onPublished }: { saved: api.PublishedRules; versions: api.RulesetEntry[]; onPublished: (rules: api.PublishedRules) => void }) {
  const t = useTranslations('documents.rules');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const baseline = saved.published ? saved.source : LOCKED_RULES;
  const [text, setText] = useState(baseline);
  // The version the editor shows; null is the published one (or the draft over it).
  const [viewing, setViewing] = useState<string | null>(null);
  const [loadingVersion, setLoadingVersion] = useState(false);
  const [checked, setChecked] = useState<Checked | null>(saved.issues?.length ? { text: baseline, issues: saved.issues } : null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const area = useRef<HTMLTextAreaElement>(null);
  const gutter = useRef<HTMLDivElement>(null);
  const pg = usePlayground(project.projectId, text);
  const dirty = text !== baseline;

  // Asks the server whether the text compiles once it has rested; an older answer is dropped.
  useEffect(() => {
    const controller = new AbortController();
    const timer = setTimeout(() => {
      api.validateRules(project.projectId, text, controller.signal).then(
        (r) => setChecked({ text, issues: r.issues ?? [] }),
        (err: unknown) => {
          if (!controller.signal.aborted) setError(err);
        },
      );
    }, VALIDATE_DELAY_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [text, project.projectId]);

  const current = checked?.text === text ? sortIssues(checked.issues) : [];
  const errors = current.filter((i) => i.severity === 'ERROR');
  const marks = lineMarks(current);
  const lineCount = text.split('\n').length;

  async function publish() {
    setPending(true);
    setError(null);
    try {
      const result = await api.publishRules(project.projectId, text);
      notify(t('publishedToast'));
      onPublished(result);
    } catch (err) {
      const issues = refusedIssues(err);
      if (issues.length > 0) setChecked({ text, issues });
      else setError(err);
      setPending(false);
    }
  }

  function discard() {
    setText(baseline);
    setViewing(null);
    setError(null);
    notify(t('discarded'));
  }

  async function view(entry: api.RulesetEntry) {
    setLoadingVersion(true);
    setError(null);
    try {
      const ruleset = await api.getRuleset(project.projectId, entry.name);
      setText(ruleset.source);
      setViewing(entry.name);
    } catch (err) {
      setError(err);
    } finally {
      setLoadingVersion(false);
    }
  }

  function goTo(issue: api.RuleIssue) {
    const el = area.current;
    const range = issueRange(text, issue);
    if (!el || !range) return;
    el.focus();
    el.setSelectionRange(range[0], range[1]);
  }

  const shownVersion = dirty && viewing === null ? 'draft' : (viewing ?? versions.find((v) => v.current)?.name ?? null);

  return (
    <div className={styles.layout}>
      <nav aria-label={t('history')}>
        <h3 className={styles.historyTitle}>{t('history')}</h3>
        <ul className={styles.history}>
          {dirty && (
            <li>
              <button type="button" className={styles.version} aria-current={shownVersion === 'draft'} onClick={() => undefined}>
                <strong>{t('rightNow')}</strong>
                <span>{t('unpublishedChanges')}</span>
              </button>
            </li>
          )}
          {versions.map((v) => (
            <li key={v.name}>
              <button
                type="button"
                className={styles.version}
                aria-current={shownVersion === v.name}
                disabled={loadingVersion}
                onClick={() => void view(v)}
                data-ruleset={rulesetId(v.name)}
              >
                <Time value={v.createTime} />
                {v.current && <Badge tone="primary">{t('published')}</Badge>}
              </button>
            </li>
          ))}
          {versions.length === 0 && <li className={styles.meta}>{t('noHistory')}</li>}
        </ul>
      </nav>
      <div className={styles.main}>
        {!saved.published ? (
          <Banner tone="info" title={t('defaultTitle')}>
            {t('defaultBody')}
          </Banner>
        ) : (
          saved.updateTime && <p className={styles.meta}>{t.rich('publishedAt', { time: () => <Time value={saved.updateTime!} /> })}</p>
        )}
        {dirty && (
          <div className={styles.bar} role="region" aria-label={t('unpublishedChanges')}>
            <span>{t('unpublishedChanges')}</span>
            {editable && (
              <div className={styles.barActions}>
                <Button isPending={pending} isDisabled={errors.length > 0 || pending} onPress={() => void publish()}>
                  {t('publish')}
                </Button>
                <Button variant="outlined" isDisabled={pending} onPress={discard}>
                  {t('discard')}
                </Button>
              </div>
            )}
          </div>
        )}
        <SimulationBanner pg={pg} />
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
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
        <AriaTextField
          value={text}
          onChange={(v) => {
            setText(v);
            setViewing(null);
          }}
          isReadOnly={!editable}
          isInvalid={errors.length > 0}
          className={styles.field}
        >
          <Label className={styles.label}>{t('editor')}</Label>
          <div className={styles.editor} dir="ltr">
            <div className={styles.gutter} ref={gutter} aria-hidden="true">
              {Array.from({ length: lineCount }, (_, i) => {
                const mark = marks.get(i + 1);
                return (
                  <span
                    key={i}
                    className={mark?.severity === 'error' ? styles.errorLine : mark?.severity === 'warning' ? styles.warningLine : undefined}
                    data-line={i + 1}
                    data-issue={mark?.severity}
                    title={mark?.message}
                  >
                    {i + 1}
                  </span>
                );
              })}
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
        <Playground pg={pg} />
      </div>
    </div>
  );
}
