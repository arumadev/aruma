'use client';

import { useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import * as api from './api';
import { allowsEveryOrigin, CORS_EXAMPLE, formatCors, parseCors } from './cors';
import styles from './CorsScreen.module.css';

/**
 * Which other sites may read the files' bytes in the browser (fetch, getBytes, getBlob): none
 * until the owner allows some, as in a new Firebase bucket. Images and video need no rule.
 */
export function CorsScreen() {
  const t = useTranslations('storage.cors');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const cors = useResource((signal) => api.getCors(project.projectId, signal), [project.projectId]);
  useDocumentTitle(t('title'));

  return (
    <Card title={t('title')} description={t('description')}>
      {cors.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={cors.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(cors.error)}
        </Banner>
      ) : !cors.data ? (
        <Skeleton lines={6} />
      ) : (
        <CorsEditor key={JSON.stringify(cors.data.cors)} saved={cors.data.cors} onSaved={(rules) => cors.setData({ cors: rules })} />
      )}
    </Card>
  );
}

function CorsEditor({ saved, onSaved }: { saved: api.CorsRule[]; onSaved: (rules: api.CorsRule[]) => void }) {
  const t = useTranslations('storage.cors');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const initial = formatCors(saved);
  const [text, setText] = useState(initial);
  const [touched, setTouched] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const parsed = parseCors(text);
  const problem = 'problem' in parsed ? parsed.problem : undefined;
  const changed = text !== initial;
  const shownProblem = problem && (touched || changed) ? t(`problem.${problem.kind}`, { rule: 'rule' in problem ? problem.rule : 0, field: 'field' in problem ? (problem.field ?? '') : '' }) : undefined;

  async function save(event: FormEvent) {
    event.preventDefault();
    setTouched(true);
    if (!('rules' in parsed)) return;
    setPending(true);
    setError(null);
    try {
      const result = await api.setCors(project.projectId, parsed.rules);
      notify(result.cors.length === 0 ? t('cleared') : t('saved', { count: result.cors.length }));
      onSaved(result.cors);
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={save} noValidate>
      {saved.length === 0 && <Banner tone="info">{t('none')}</Banner>}
      {'rules' in parsed && allowsEveryOrigin(parsed.rules) && (
        <Banner tone="warning" title={t('everyOriginTitle')}>
          {t('everyOriginBody')}
        </Banner>
      )}
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      <TextField
        label={t('editor')}
        description={t('editorHint')}
        value={text}
        onChange={setText}
        isReadOnly={!editable}
        multiline
        rows={Math.min(Math.max(text.split('\n').length, 8), 24)}
        ltr
        errorMessage={shownProblem}
        className={styles.editor}
      />
      {editable && (
        <div className={styles.actions}>
          <Button type="submit" isPending={pending} isDisabled={!changed}>
            {t('save')}
          </Button>
          {saved.length === 0 && text.trim() === '[]' && (
            <Button variant="outlined" icon="add" onPress={() => setText(formatCors(CORS_EXAMPLE))}>
              {t('example')}
            </Button>
          )}
          <Button variant="text" icon="undo" isDisabled={!changed || pending} onPress={() => setText(initial)}>
            {t('revert')}
          </Button>
        </div>
      )}
    </form>
  );
}
