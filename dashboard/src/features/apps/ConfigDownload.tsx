'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { CodeBlock } from '@/components/ui/Code';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import * as api from '@/lib/api/console';
import type { App } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import styles from './ConfigDownload.module.css';
import { PLATFORMS } from './platforms';

const WEB_INIT = `import { initializeApp } from 'firebase/app';

const app = initializeApp(firebaseConfig);`;

/**
 * The app's config file as a download, the web snippet when it is a web app, and where to put
 * the file. Used at the end of the wizard and on the app's page.
 */
export function ConfigDownload({ projectId, app }: { projectId: string; app: App }) {
  const t = useTranslations('config');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const web = app.platform === 'WEB';
  const snippet = useResource((signal) => api.getWebSnippet(projectId, app.appId, signal), [projectId, app.appId], web);
  const file = PLATFORMS[app.platform].configFile;

  async function download() {
    setPending(true);
    setError(null);
    try {
      const name = await api.downloadAppConfig(projectId, app.appId);
      notify(t('downloaded', { file: name }));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  return (
    <div className={styles.config}>
      <div className={styles.download}>
        <Button icon="download" isPending={pending} onPress={download}>
          {t('download', { file })}
        </Button>
      </div>
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      {app.platform === 'ANDROID' && <p className={styles.note}>{t('androidShared')}</p>}
      <ol className={styles.steps}>
        {app.platform === 'ANDROID' && (
          <>
            <li>{t.rich('android1', { file: (c) => <code dir="ltr">{c}</code> })}</li>
            <li>{t.rich('android2', { file: (c) => <code dir="ltr">{c}</code> })}</li>
          </>
        )}
        {app.platform === 'IOS' && (
          <>
            <li>{t.rich('ios1', { file: (c) => <code dir="ltr">{c}</code> })}</li>
            <li>{t.rich('ios2', { file: (c) => <code dir="ltr">{c}</code> })}</li>
          </>
        )}
        {web && (
          <>
            <li>{t.rich('web1', { file: (c) => <code dir="ltr">{c}</code> })}</li>
            <li>{t('web2')}</li>
          </>
        )}
      </ol>
      {web &&
        (snippet.error ? (
          <Banner tone="error">{errorText.message(snippet.error)}</Banner>
        ) : snippet.data ? (
          <>
            <CodeBlock label={t('snippetLabel')} code={snippet.data.trimEnd()} copyLabel={t('copySnippet')} />
            <CodeBlock label={t('initLabel')} code={WEB_INIT} copyLabel={t('copyInit')} />
          </>
        ) : (
          <Skeleton lines={3} />
        ))}
    </div>
  );
}
