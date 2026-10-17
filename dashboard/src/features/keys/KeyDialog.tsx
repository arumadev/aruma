'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { Dialog } from '@/components/ui/Dialog';
import { Select } from '@/components/ui/Select';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import * as api from '@/lib/api/console';
import type { ApiKey } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { DISPLAY_NAME_MAX } from '@/lib/validation';
import styles from './KeyDialog.module.css';
import { fromDraft, toDraft, validateDraft, type RestrictionDraft, type RestrictionKind } from './restrictions';

/** Creates a key, or edits a key's name and application restrictions. */
export function KeyDialog({ projectId, apiKey, onSaved, onClose }: { projectId: string; apiKey?: ApiKey; onSaved: (key: ApiKey) => void; onClose: () => void }) {
  const t = useTranslations('keys');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const [name, setName] = useState(apiKey?.displayName ?? '');
  const [draft, setDraft] = useState<RestrictionDraft>(() => toDraft(apiKey?.restrictions ?? {}));
  const [attempted, setAttempted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const problem = attempted ? validateDraft(draft) : null;

  async function save() {
    setAttempted(true);
    if (validateDraft(draft)) return;
    setPending(true);
    setError(null);
    const body = { displayName: name.trim(), restrictions: fromDraft(draft, apiKey?.restrictions) };
    try {
      const saved = apiKey ? await api.updateKey(projectId, apiKey.uid, { ...body, etag: apiKey.etag }) : await api.createKey(projectId, body);
      onSaved(saved);
      notify(apiKey ? t('saved') : t('created'));
      onClose();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  const update = (patch: Partial<RestrictionDraft>) => setDraft((d) => ({ ...d, ...patch }));
  const kinds: RestrictionKind[] = ['none', 'browser', 'android', 'ios'];

  return (
    <Dialog
      isOpen
      wide
      onOpenChange={(open) => !open && onClose()}
      title={apiKey ? t('editTitle') : t('createTitle')}
      isBusy={pending}
      onSubmit={save}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" isPending={pending}>
            {apiKey ? tCommon('save') : t('create')}
          </Button>
        </>
      }
    >
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      {apiKey && (
        <p>
          <Ltr>{apiKey.keyString}</Ltr>
        </p>
      )}
      <TextField label={t('name')} value={name} onChange={setName} maxLength={DISPLAY_NAME_MAX} autoFocus={!apiKey} description={t('nameHint')} />
      <Select
        label={t('restrictionKind')}
        value={draft.kind}
        onChange={(kind) => update({ kind: kind as RestrictionKind })}
        options={kinds.map((k) => ({ id: k, label: t(`kind.${k}`) }))}
        description={t(`kindHint.${draft.kind}`)}
      />
      {(draft.kind === 'browser' || draft.kind === 'ios') && (
        <TextField
          label={draft.kind === 'browser' ? t('referrers') : t('bundleIds')}
          multiline
          rows={4}
          ltr
          value={draft.lines}
          onChange={(lines) => update({ lines })}
          placeholder={draft.kind === 'browser' ? 'https://example.com/*' : 'com.example.app'}
          description={t('onePerLine')}
          errorMessage={problem === 'empty' ? t('needOne') : problem === 'bundleId' ? t('badBundleId') : undefined}
        />
      )}
      {draft.kind === 'android' && (
        <fieldset className={styles.android}>
          <legend className={styles.legend}>{t('androidApps')}</legend>
          {draft.android.map((app, i) => (
            <div key={i} className={styles.androidRow}>
              <TextField
                className={styles.androidField}
                label={t('packageName')}
                ltr
                value={app.packageName}
                onChange={(packageName) => update({ android: draft.android.map((a, j) => (j === i ? { ...a, packageName } : a)) })}
              />
              <TextField
                className={styles.androidField}
                label={t('sha1')}
                ltr
                value={app.sha1Fingerprint}
                onChange={(sha1Fingerprint) => update({ android: draft.android.map((a, j) => (j === i ? { ...a, sha1Fingerprint } : a)) })}
              />
              <IconButton
                icon="delete"
                label={t('removeApp')}
                className={styles.remove}
                onPress={() => update({ android: draft.android.filter((_, j) => j !== i) })}
              />
            </div>
          ))}
          {problem && problem !== 'bundleId' && (
            <p className={styles.problem} role="alert">
              {t(`problem.${problem}`)}
            </p>
          )}
          <div>
            <Button variant="text" icon="add" onPress={() => update({ android: [...draft.android, { packageName: '', sha1Fingerprint: '' }] })}>
              {t('addAndroidApp')}
            </Button>
          </div>
        </fieldset>
      )}
      {apiKey?.restrictions.apiTargets && apiKey.restrictions.apiTargets.length > 0 && (
        <p className={styles.targets}>
          {t('apiTargets')} <Ltr>{apiKey.restrictions.apiTargets.map((a) => a.service).join(', ')}</Ltr>
        </p>
      )}
    </Dialog>
  );
}
