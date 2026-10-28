'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { Switch } from '@/components/ui/Switch';
import { TextField } from '@/components/ui/TextField';
import { useErrorText } from '@/lib/errors';
import * as api from '../api';
import styles from './Playground.module.css';
import { authPayload, cleanPath, METHODS, methodNoun, parseData, pathProblem, PROVIDERS, refusedIssues } from './rulesText';

interface Run {
  /** What the simulation was run on: the request and the rules text. */
  key: string;
  result?: api.SimulateResult;
  error?: unknown;
  dismissed?: boolean;
}

/**
 * The Playground's form and its last run. It lives apart from the panel so the result's banner
 * can sit over the editor, as in the Firebase console, and be marked outdated when the inputs
 * or the rules change after it ran.
 */
export function usePlayground(project: string, text: string) {
  const [method, setMethod] = useState<api.SimulateMethod>('get');
  const [path, setPath] = useState('');
  const [touched, setTouched] = useState(false);
  const [authed, setAuthed] = useState(false);
  const [auth, setAuth] = useState<api.SimulateAuth>({ provider: 'google.com', emailVerified: false });
  const [dataText, setDataText] = useState('{}');
  const [run, setRun] = useState<Run | null>(null);
  const [pending, setPending] = useState(false);

  const withData = method === 'create' || method === 'update';
  const data = withData ? parseData(dataText) : { fields: undefined };
  const pathError = pathProblem(path);
  const dataError = 'problem' in data ? data.problem : undefined;
  const key = JSON.stringify([method, cleanPath(path), authed ? auth : null, withData ? dataText : null, text]);

  async function execute() {
    setTouched(true);
    if (pathError || dataError) return;
    setPending(true);
    const request: api.SimulateRequest = {
      source: text,
      method,
      path: cleanPath(path),
      ...(authed && { auth }),
      ...(withData && 'fields' in data && { data: data.fields }),
    };
    try {
      setRun({ key, result: await api.simulateRules(project, request) });
    } catch (error) {
      setRun({ key, error });
    } finally {
      setPending(false);
    }
  }

  return {
    method, setMethod, path, setPath, authed, setAuthed, auth, setAuth, dataText, setDataText, withData,
    pathError: touched ? pathError : undefined, dataError, pending, execute, run,
    outdated: run !== null && run.key !== key,
    dismiss: () => setRun((r) => (r ? { ...r, dismissed: true } : r)),
    project,
  };
}

export type PlaygroundState = ReturnType<typeof usePlayground>;

/** The banner over the editor: allowed or denied, the simulation's age, and its details. */
export function SimulationBanner({ pg }: { pg: PlaygroundState }) {
  const t = useTranslations('documents.rules.playground');
  const errorText = useErrorText();
  const { run } = pg;
  if (!run || run.dismissed) return null;
  const dismiss = (
    <Button variant="text" onPress={pg.dismiss}>
      {t('dismiss')}
    </Button>
  );
  if (run.error !== undefined) {
    const issues = refusedIssues(run.error);
    return (
      <Banner tone="error" title={t('failed')} action={dismiss}>
        {issues.length > 0 ? issues.map((i, n) => <bdi key={n} dir="ltr">{i.description}</bdi>) : errorText.message(run.error)}
      </Banner>
    );
  }
  const result = run.result!;
  const noun = t(`noun.${methodNoun(pg.method)}`);
  return (
    <div className={styles.result} data-allowed={result.allowed} data-testid="simulation">
      <p className={styles.verdict} role="status">
        {result.allowed ? t('allowed', { method: noun }) : t('denied', { method: noun })}
        {pg.outdated && <span> {t('outdated')}</span>}
      </p>
      {dismiss}
    </div>
  );
}

/** The panel's controls: the request, the user, the data, and Run. */
export function Playground({ pg }: { pg: PlaygroundState }) {
  const t = useTranslations('documents.rules.playground');
  const [open, setOpen] = useState(false);
  const detail = pg.run?.result;

  return (
    <section className={styles.panel} aria-label={t('title')}>
      <button type="button" className={styles.toggle} aria-expanded={open} aria-controls="rules-playground" onClick={() => setOpen(!open)}>
        <span className={styles.toggleTitle}>{t('title')}</span>
        <span className={styles.toggleHint}>{open ? t('collapse') : t('expand')}</span>
      </button>
      {open && (
        <form
          id="rules-playground"
          className={styles.form}
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void pg.execute();
          }}
        >
          <Select
            label={t('simulationType')}
            value={pg.method}
            onChange={(id) => pg.setMethod(id as api.SimulateMethod)}
            options={METHODS.map((m) => ({ id: m, label: t(`method.${m}`) }))}
          />
          <TextField
            label={t('location')}
            value={pg.path}
            onChange={pg.setPath}
            ltr
            placeholder={t('pathPlaceholder')}
            description={
              <>
                {t('locationPrefix')} <bdi dir="ltr">{'/databases/(default)/documents/'}</bdi>
              </>
            }
            errorMessage={pg.pathError && t(`pathProblem.${pg.pathError}`)}
          />
          <Switch isSelected={pg.authed} onChange={pg.setAuthed}>
            {t('authenticated')}
          </Switch>
          {pg.authed && (
            <fieldset className={styles.auth}>
              <legend className={styles.legend}>{t('authLegend')}</legend>
              <Banner tone="info">{t('authHint')}</Banner>
              <Select
                label={t('provider')}
                value={pg.auth.provider ?? 'google.com'}
                onChange={(id) => pg.setAuth({ ...pg.auth, provider: id })}
                options={PROVIDERS.map((p) => ({ id: p, label: p }))}
              />
              <TextField label={t('uid')} ltr value={pg.auth.uid ?? ''} onChange={(v) => pg.setAuth({ ...pg.auth, uid: v })} />
              <TextField label={t('email')} ltr value={pg.auth.email ?? ''} onChange={(v) => pg.setAuth({ ...pg.auth, email: v })} />
              <Switch isSelected={pg.auth.emailVerified ?? false} onChange={(v) => pg.setAuth({ ...pg.auth, emailVerified: v })}>
                {t('emailVerified')}
              </Switch>
              <TextField label={t('name')} value={pg.auth.name ?? ''} onChange={(v) => pg.setAuth({ ...pg.auth, name: v })} />
              <TextField label={t('phone')} ltr value={pg.auth.phoneNumber ?? ''} onChange={(v) => pg.setAuth({ ...pg.auth, phoneNumber: v })} />
              <TextField
                label={t('payload')}
                ltr
                multiline
                rows={9}
                isReadOnly
                value={JSON.stringify(authPayload(pg.auth, pg.project), null, 2)}
              />
            </fieldset>
          )}
          {pg.withData && (
            <TextField
              label={t('data')}
              ltr
              multiline
              rows={6}
              value={pg.dataText}
              onChange={pg.setDataText}
              description={t('dataHint')}
              errorMessage={pg.dataError && t(`dataProblem.${pg.dataError}`)}
            />
          )}
          <div className={styles.actions}>
            <Button type="submit" icon="bolt" isPending={pg.pending}>
              {t('run')}
            </Button>
          </div>
          {detail && (
            <div className={styles.detail}>
              <h3 className={styles.detailTitle}>{t('detail')}</h3>
              {detail.statements.length === 0 ? (
                <p>{t('noStatements')}</p>
              ) : (
                <ul className={styles.statements}>
                  {detail.statements.map((s, i) => (
                    <li key={i} className={styles.statement} data-outcome={s.outcome}>
                      <span>{t('statement', { line: s.line, column: s.column })}</span>{' '}
                      <bdi dir="ltr" className={styles.code}>
                        {`allow ${s.methods.join(', ')}`}
                      </bdi>{' '}
                      <strong>{t(`outcome.${s.outcome}`)}</strong>
                      {s.message && (
                        <>
                          {' '}
                          <bdi dir="ltr" className={styles.code}>
                            {s.message}
                          </bdi>
                        </>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </form>
      )}
    </section>
  );
}
