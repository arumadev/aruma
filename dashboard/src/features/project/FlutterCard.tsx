'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { CodeBlock } from '@/components/ui/Code';
import { Select } from '@/components/ui/Select';
import { useSnackbar } from '@/components/ui/Snackbar';
import { PLATFORM_ORDER, PLATFORMS } from '@/features/apps/platforms';
import * as api from '@/lib/api/console';
import type { App, FlutterSelection, Platform } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import styles from './FlutterCard.module.css';

const NONE = 'none';
const PARAM: Record<Platform, keyof FlutterSelection> = { WEB: 'web', ANDROID: 'android', IOS: 'ios' };
const INIT_SNIPPET = `await Firebase.initializeApp(
  options: DefaultFirebaseOptions.currentPlatform,
);`;

/** Builds firebase_options.dart from one app per platform. */
export function FlutterCard({ projectId, apps }: { projectId: string; apps: App[] }) {
  const t = useTranslations('flutter');
  const tPlatform = useTranslations('platform');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const platforms = PLATFORM_ORDER.filter((p) => apps.some((a) => a.platform === p));
  const [choice, setChoice] = useState<Partial<Record<Platform, string>>>({});
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  // Each platform defaults to its first app until the user picks.
  const selected = (p: Platform) => choice[p] ?? apps.find((a) => a.platform === p)?.appId ?? NONE;
  const selection: FlutterSelection = {};
  for (const p of platforms) {
    const id = selected(p);
    if (id !== NONE) selection[PARAM[p]] = id;
  }
  const empty = Object.keys(selection).length === 0;

  async function download() {
    setPending(true);
    setError(null);
    try {
      const name = await api.downloadFlutterOptions(projectId, selection);
      notify(t('downloaded', { file: name }));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  return (
    <Card title={t('title')} description={t('description')}>
      <div className={styles.selects}>
        {platforms.map((p) => (
          <Select
            key={p}
            label={tPlatform(PLATFORMS[p].label)}
            value={selected(p)}
            onChange={(id) => setChoice((c) => ({ ...c, [p]: id }))}
            options={[
              { id: NONE, label: t('none') },
              ...apps.filter((a) => a.platform === p).map((a) => ({ id: a.appId, label: a.displayName || a.appId, detail: a.appId })),
            ]}
          />
        ))}
      </div>
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      <div>
        <Button variant="tonal" icon="download" isDisabled={empty} isPending={pending} onPress={download}>
          {t('download')}
        </Button>
      </div>
      <p className={styles.hint}>{t('placement')}</p>
      <CodeBlock label={t('initLabel')} code={INIT_SNIPPET} copyLabel={t('copyInit')} />
    </Card>
  );
}
