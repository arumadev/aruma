'use client';

import { useRef, useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, LinkButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { PageHeader } from '@/components/ui/PageHeader';
import { RadioCards } from '@/components/ui/RadioCards';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { useProject } from '@/features/project/ProjectContext';
import { ApiError } from '@/lib/api/client';
import * as api from '@/lib/api/console';
import type { App, Platform } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { routes } from '@/lib/routes';
import { BUNDLE_ID, DISPLAY_NAME_MAX, PACKAGE_NAME } from '@/lib/validation';
import { ConfigDownload } from './ConfigDownload';
import styles from './NewAppScreen.module.css';
import { PLATFORM_ORDER, PLATFORMS } from './platforms';

type Step = 'platform' | 'identifier' | 'name' | 'done';

const stepsFor = (platform: Platform | null): Step[] =>
  platform === 'WEB' ? ['platform', 'name', 'done'] : ['platform', 'identifier', 'name', 'done'];

/**
 * D-06: register an app in steps: platform, store identifier (Android and iOS; it can never
 * change), nickname, then the config file and how to use it.
 */
export function NewAppScreen() {
  const t = useTranslations('newApp');
  const tPlatform = useTranslations('platform');
  const tErrors = useTranslations('errors');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const [platform, setPlatform] = useState<Platform | null>(null);
  const [step, setStep] = useState<Step>('platform');
  const [identifier, setIdentifier] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [attempted, setAttempted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [created, setCreated] = useState<App | null>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  useDocumentTitle(t('title'));

  const steps = stepsFor(platform);
  const index = steps.indexOf(step);
  const android = platform === 'ANDROID';
  const identifierField = android ? 'packageName' : 'bundleId';
  const identifierValid = (android ? PACKAGE_NAME : BUNDLE_ID).test(identifier.trim());
  const identifierTaken = error instanceof ApiError && error.status === 'ALREADY_EXISTS';

  function go(next: Step) {
    setAttempted(false);
    setStep(next);
    // Move focus to the new step's heading so keyboard and screen reader users follow along.
    requestAnimationFrame(() => headingRef.current?.focus());
  }

  async function advance(event: FormEvent) {
    event.preventDefault();
    setAttempted(true);
    if (step === 'platform' && platform) return go(steps[1]!);
    if (step === 'identifier' && identifierValid) return go('name');
    if (step === 'name' && platform) {
      setPending(true);
      setError(null);
      try {
        const app = await api.createApp(project.projectId, {
          platform,
          displayName: displayName.trim() || undefined,
          ...(platform === 'ANDROID' && { packageName: identifier.trim() }),
          ...(platform === 'IOS' && { bundleId: identifier.trim() }),
        });
        setCreated(app);
        notify(t('created'));
        go('done');
      } catch (err) {
        setError(err);
        // A duplicate or malformed identifier is fixed on its own step.
        const taken = err instanceof ApiError && err.status === 'ALREADY_EXISTS';
        if (taken || errorText.field(err, identifierField)) go('identifier');
      } finally {
        setPending(false);
      }
    }
  }

  const identifierError = !identifier.trim()
    ? attempted
      ? t('identifierRequired')
      : undefined
    : attempted && !identifierValid
      ? tErrors(`field.${identifierField}`)
      : identifierTaken
        ? t(android ? 'packageTaken' : 'bundleTaken')
        : errorText.field(error, identifierField);
  const formError = error && !identifierTaken && !errorText.field(error, identifierField) && !errorText.field(error, 'displayName') ? errorText.message(error) : null;

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle', { project: project.displayName })} />
      <ol className={styles.stepper} aria-label={t('progress')}>
        {steps.map((s, i) => (
          <li key={s} className={styles.stepItem} aria-current={s === step ? 'step' : undefined} data-done={i < index || undefined}>
            <span className={styles.stepNumber}>{i + 1}</span>
            <span className={styles.stepLabel}>{t(`step.${s}`)}</span>
          </li>
        ))}
      </ol>
      <Card>
        <form className={styles.form} onSubmit={advance} noValidate>
          <h2 ref={headingRef} tabIndex={-1} className={styles.stepTitle}>
            {t('stepOf', { current: index + 1, total: steps.length })} · {t(`step.${step}`)}
          </h2>
          {formError && <Banner tone="error">{formError}</Banner>}

          {step === 'platform' && (
            <>
              <RadioCards
                label={t('platformLabel')}
                value={platform}
                onChange={(value) => setPlatform(value as Platform)}
                options={PLATFORM_ORDER.map((p) => ({
                  id: p,
                  icon: PLATFORMS[p].icon,
                  label: tPlatform(PLATFORMS[p].label),
                  description: t(`platformHint.${PLATFORMS[p].label}`),
                }))}
              />
              {attempted && !platform && <Banner tone="error">{t('platformRequired')}</Banner>}
            </>
          )}

          {step === 'identifier' && (
            <>
              <Banner tone="warning" title={t('permanentTitle')}>
                {t(android ? 'permanentAndroid' : 'permanentIos')}
              </Banner>
              <TextField
                label={t(android ? 'packageName' : 'bundleId')}
                value={identifier}
                onChange={(value) => {
                  setIdentifier(value);
                  setError(null);
                }}
                ltr
                isRequired
                autoFocus
                placeholder="com.example.app"
                description={t(android ? 'packageHint' : 'bundleHint')}
                errorMessage={identifierError}
              />
            </>
          )}

          {step === 'name' && (
            <>
              {platform !== 'WEB' && (
                <p className={styles.summary}>
                  {t(android ? 'packageName' : 'bundleId')}: <Ltr>{identifier.trim()}</Ltr>
                </p>
              )}
              <TextField
                label={t('nickname')}
                value={displayName}
                onChange={setDisplayName}
                maxLength={DISPLAY_NAME_MAX}
                autoFocus
                description={t('nicknameHint')}
                errorMessage={errorText.field(error, 'displayName')}
              />
            </>
          )}

          {step === 'done' && created && (
            <>
              <p className={styles.summary}>
                {t('doneBody')} <Ltr>{created.appId}</Ltr>
              </p>
              <ConfigDownload projectId={project.projectId} app={created} />
            </>
          )}

          <div className={styles.actions}>
            {step === 'done' && created ? (
              <>
                <LinkButton variant="text" href={routes.project(project.projectId)}>
                  {t('toOverview')}
                </LinkButton>
                <LinkButton href={routes.app(project.projectId, created.appId)}>{t('toApp')}</LinkButton>
              </>
            ) : (
              <>
                {index > 0 ? (
                  <Button variant="text" icon="arrowBack" onPress={() => go(steps[index - 1]!)} isDisabled={pending}>
                    {t('back')}
                  </Button>
                ) : (
                  <LinkButton variant="text" href={routes.project(project.projectId)}>
                    {t('cancel')}
                  </LinkButton>
                )}
                <Button type="submit" isPending={pending}>
                  {step === 'name' ? t('register') : t('next')}
                </Button>
              </>
            )}
          </div>
        </form>
      </Card>
    </>
  );
}
