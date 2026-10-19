'use client';

import { useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button, IconButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Switch } from '@/components/ui/Switch';
import { TextField } from '@/components/ui/TextField';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { AuthConfig, AuthConfigUpdate, FederatedProvider, FederatedProviderUpdate, PasswordEnforcement } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { isValidDomain, normalizeDomain } from './domains';
import { SettingSwitch } from './SettingSwitch';
import styles from './SignInMethodsScreen.module.css';

const MIN_LENGTHS = Array.from({ length: 25 }, (_, i) => String(i + 6));

/** How users sign in: providers, authorised domains, protections and the password policy. */
export function SignInMethodsScreen() {
  const t = useTranslations('auth.methods');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const config = useResource((signal) => api.getAuthConfig(project.projectId, signal), [project.projectId]);
  useDocumentTitle(t('title'));

  if (config.error && !config.data) {
    return (
      <Banner tone="error" action={<Button variant="text" onPress={config.reload}>{tCommon('retry')}</Button>}>
        {errorText.message(config.error)}
      </Banner>
    );
  }
  if (!config.data) return <Skeleton lines={3} blocks={2} />;
  return <Methods config={config.data} onSaved={config.setData} />;
}

function Methods({ config, onSaved }: { config: AuthConfig; onSaved: (config: AuthConfig) => void }) {
  const t = useTranslations('auth.methods');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const [pending, setPending] = useState<keyof AuthConfigUpdate | null>(null);
  const [error, setError] = useState<unknown>(null);

  // Switches save at once, one setting at a time.
  async function toggle(field: keyof AuthConfigUpdate, value: boolean) {
    setPending(field);
    setError(null);
    try {
      onSaved(await api.updateAuthConfig(project.projectId, { [field]: value }));
      notify(t('saved'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(null);
    }
  }

  const switchProps = (field: 'passwordEnabled' | 'anonymousEnabled' | 'enumerationProtection', label: string, description: string) => ({
    label,
    description,
    isSelected: config[field],
    isDisabled: !editable || pending !== null,
    onChange: (value: boolean) => void toggle(field, value),
  });

  return (
    <>
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      <Card title={t('providers')} description={t('providersHint')}>
        <div className={styles.settings}>
          <SettingSwitch {...switchProps('passwordEnabled', t('password'), t('passwordHint'))} />
          <SettingSwitch {...switchProps('anonymousEnabled', t('anonymous'), t('anonymousHint'))} />
        </div>
      </Card>
      <FederatedProviders editable={editable} />
      <DomainsCard config={config} editable={editable} onSaved={onSaved} />
      <Card title={t('protection')}>
        <div className={styles.settings}>
          <SettingSwitch {...switchProps('enumerationProtection', t('enumeration'), t('enumerationHint'))} />
          <SettingSwitch
            label={t('blockSignup')}
            description={t('blockSignupHint')}
            isSelected={!config.clientSignupEnabled}
            isDisabled={!editable || pending !== null}
            onChange={(block) => void toggle('clientSignupEnabled', !block)}
          />
        </div>
      </Card>
      <PasswordPolicyCard key={policyKey(config)} config={config} editable={editable} onSaved={onSaved} />
    </>
  );
}

/** Google and Apple, with the redirect URI to register with both. */
function FederatedProviders({ editable }: { editable: boolean }) {
  const t = useTranslations('auth.federated');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const providers = useResource((signal) => api.listFederatedProviders(project.projectId, signal), [project.projectId]);

  if (providers.error) {
    return (
      <Banner tone="error" action={<Button variant="text" onPress={providers.reload}>{tCommon('retry')}</Button>}>
        {errorText.message(providers.error)}
      </Banner>
    );
  }
  if (!providers.data) return <Skeleton lines={2} />;
  const replace = (saved: FederatedProvider) =>
    providers.setData((current) => ({
      redirectUri: current?.redirectUri ?? '',
      providers: (current?.providers ?? []).map((p) => (p.providerId === saved.providerId ? saved : p)),
    }));
  return (
    <>
      <Card title={t('redirectTitle')} description={t('redirectHint')}>
        <DetailList>
          <Detail label={t('redirectUri')} value={providers.data.redirectUri} copyLabel={t('copyRedirectUri')} />
        </DetailList>
      </Card>
      {providers.data.providers.map((p) => (
        <FederatedCard key={`${p.providerId}:${JSON.stringify(p)}`} provider={p} editable={editable} onSaved={replace} />
      ))}
    </>
  );
}

// Apple's Team ID and Key ID are 10 upper-case letters or digits.
const APPLE_ID = /^[A-Z0-9]{10}$/;

type FederatedField = 'clientId' | 'servicesId' | 'teamId' | 'keyId';

function FederatedCard({ provider, editable, onSaved }: { provider: FederatedProvider; editable: boolean; onSaved: (p: FederatedProvider) => void }) {
  const t = useTranslations('auth.federated');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const google = provider.providerId === 'google.com';
  const [enabled, setEnabled] = useState(provider.enabled);
  const [fields, setFields] = useState<Record<FederatedField, string>>({
    clientId: provider.clientId ?? '',
    servicesId: provider.servicesId ?? '',
    teamId: provider.teamId ?? '',
    keyId: provider.keyId ?? '',
  });
  // Secrets start empty: the server never sends them back. Empty means "keep the saved one".
  const [secret, setSecret] = useState('');
  const [attempted, setAttempted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const secretSaved = google ? provider.clientSecretSet : provider.privateKeySet;

  // The server's rules (INVALID_CONFIG), checked first so each field can say what is wrong.
  const problems: Partial<Record<FederatedField | 'secret', string>> = {};
  const required: FederatedField[] = google ? ['clientId'] : ['servicesId', 'teamId', 'keyId'];
  for (const name of required) if (enabled && !fields[name].trim()) problems[name] = t('required');
  if (!google) {
    for (const name of ['teamId', 'keyId'] as const) if (fields[name].trim() && !APPLE_ID.test(fields[name].trim())) problems[name] = t('appleId');
    if (secret && !secret.includes('BEGIN PRIVATE KEY')) problems.secret = t('privateKeyFormat');
  }
  if (enabled && !secret && !secretSaved) problems.secret = t('required');
  const valid = Object.keys(problems).length === 0;

  async function save(event: FormEvent) {
    event.preventDefault();
    setAttempted(true);
    if (!valid) return;
    setPending(true);
    setError(null);
    const body: FederatedProviderUpdate = google
      ? { enabled, clientId: fields.clientId.trim(), ...(secret && { clientSecret: secret }) }
      : { enabled, servicesId: fields.servicesId.trim(), teamId: fields.teamId.trim(), keyId: fields.keyId.trim(), ...(secret && { privateKey: secret }) };
    try {
      onSaved(await api.updateFederatedProvider(project.projectId, provider.providerId, body));
      setSecret('');
      setAttempted(false);
      notify(t('saved'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  const shown = (name: FederatedField | 'secret') => (attempted ? problems[name] : undefined);
  const field = (name: FederatedField, label: string, description?: string) => (
    <TextField
      label={label}
      ltr
      value={fields[name]}
      onChange={(v) => setFields((f) => ({ ...f, [name]: v }))}
      isReadOnly={!editable}
      description={description}
      errorMessage={shown(name)}
    />
  );
  return (
    <Card
      title={google ? t('google') : t('apple')}
      description={google ? t('googleHint') : t('appleHint')}
      actions={provider.enabled ? <Badge tone="primary">{t('on')}</Badge> : <Badge>{t('off')}</Badge>}
    >
      <form className={styles.form} onSubmit={save} noValidate>
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
        <Switch isSelected={enabled} onChange={setEnabled} isDisabled={!editable}>
          {t('enable')}
        </Switch>
        {google ? (
          field('clientId', t('clientId'))
        ) : (
          <>
            {field('servicesId', t('servicesId'), t('servicesIdHint'))}
            {field('teamId', t('teamId'))}
            {field('keyId', t('keyId'))}
          </>
        )}
        <TextField
          label={google ? t('clientSecret') : t('privateKey')}
          type={google ? 'password' : undefined}
          multiline={!google}
          rows={4}
          ltr
          autoComplete="off"
          spellCheck="false"
          placeholder={google ? undefined : '-----BEGIN PRIVATE KEY-----'}
          value={secret}
          onChange={setSecret}
          isReadOnly={!editable}
          description={secretSaved ? t('secretSaved') : t('secretMissing')}
          errorMessage={shown('secret')}
        />
        {editable && (
          <div>
            <Button type="submit" isPending={pending}>
              {tCommon('save')}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}

/** Where sign-in may happen and email links may lead: one host name or address per entry. */
function DomainsCard({ config, editable, onSaved }: { config: AuthConfig; editable: boolean; onSaved: (c: AuthConfig) => void }) {
  const t = useTranslations('auth.domains');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const [draft, setDraft] = useState('');
  const [problem, setProblem] = useState<string | undefined>();
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);

  async function save(domains: string[], busy: string) {
    setPending(busy);
    setError(null);
    try {
      onSaved(await api.updateAuthConfig(project.projectId, { authorizedDomains: domains }));
      return true;
    } catch (err) {
      setError(err);
      return false;
    } finally {
      setPending(null);
    }
  }

  async function add(event: FormEvent) {
    event.preventDefault();
    const domain = normalizeDomain(draft);
    const invalid = !isValidDomain(domain) ? t('invalid') : config.authorizedDomains.includes(domain) ? t('duplicate') : undefined;
    setProblem(invalid);
    if (invalid) return;
    if (await save([...config.authorizedDomains, domain], '+')) {
      setDraft('');
      notify(t('added', { domain }));
    }
  }

  async function remove(domain: string) {
    if (await save(config.authorizedDomains.filter((d) => d !== domain), domain)) notify(t('removed', { domain }));
  }

  return (
    <Card title={t('title')} description={t('description')}>
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      {config.authorizedDomains.length === 0 ? (
        <p className={styles.muted}>{t('empty')}</p>
      ) : (
        <ul className={styles.domains} aria-label={t('title')}>
          {config.authorizedDomains.map((domain) => (
            <li key={domain} className={styles.domain}>
              <Ltr>{domain}</Ltr>
              {editable && (
                <IconButton icon="delete" label={t('remove', { domain })} isDisabled={pending !== null} onPress={() => void remove(domain)} />
              )}
            </li>
          ))}
        </ul>
      )}
      {editable && (
        <form className={styles.addDomain} onSubmit={add} noValidate>
          <TextField
            className={styles.domainField}
            label={t('add')}
            ltr
            placeholder="app.example.com"
            value={draft}
            onChange={(v) => {
              setDraft(v);
              setProblem(undefined);
            }}
            errorMessage={problem}
          />
          <Button type="submit" variant="tonal" icon="add" isPending={pending === '+'} isDisabled={!draft.trim()}>
            {t('addButton')}
          </Button>
        </form>
      )}
    </Card>
  );
}

// The policy form starts over when the saved policy changes, not on other settings.
const policyKey = (c: AuthConfig) =>
  [c.passwordMinLength, c.passwordEnforcement, c.passwordRequireLowercase, c.passwordRequireUppercase, c.passwordRequireNumeric, c.passwordRequireSymbol].join();

function PasswordPolicyCard({ config, editable, onSaved }: { config: AuthConfig; editable: boolean; onSaved: (c: AuthConfig) => void }) {
  const t = useTranslations('auth.policy');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const initial = {
    passwordMinLength: config.passwordMinLength,
    passwordEnforcement: config.passwordEnforcement,
    passwordRequireLowercase: config.passwordRequireLowercase,
    passwordRequireUppercase: config.passwordRequireUppercase,
    passwordRequireNumeric: config.passwordRequireNumeric,
    passwordRequireSymbol: config.passwordRequireSymbol,
  };
  const [policy, setPolicy] = useState(initial);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const changed = JSON.stringify(policy) !== JSON.stringify(initial);
  const enforced = policy.passwordEnforcement === 'ENFORCE';

  async function save(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      onSaved(await api.updateAuthConfig(project.projectId, policy));
      notify(t('saved'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  const requirement = (field: 'passwordRequireLowercase' | 'passwordRequireUppercase' | 'passwordRequireNumeric' | 'passwordRequireSymbol', label: string) => (
    <Switch isSelected={policy[field]} isDisabled={!editable || !enforced} onChange={(v) => setPolicy((p) => ({ ...p, [field]: v }))}>
      {label}
    </Switch>
  );

  return (
    <Card title={t('title')} description={t('description')}>
      <form className={styles.form} onSubmit={save} noValidate>
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
        <div className={styles.row}>
          <Select
            className={styles.select}
            label={t('minLength')}
            value={String(policy.passwordMinLength)}
            onChange={(v) => setPolicy((p) => ({ ...p, passwordMinLength: Number(v) }))}
            options={MIN_LENGTHS.map((n) => ({ id: n, label: n }))}
            isDisabled={!editable}
          />
          <Select
            className={styles.select}
            label={t('enforcement')}
            value={policy.passwordEnforcement}
            onChange={(v) => setPolicy((p) => ({ ...p, passwordEnforcement: v as PasswordEnforcement }))}
            options={[
              { id: 'OFF', label: t('enforcementOff') },
              { id: 'ENFORCE', label: t('enforcementOn') },
            ]}
            description={enforced ? t('enforcementOnHint') : t('enforcementOffHint')}
            isDisabled={!editable}
          />
        </div>
        <fieldset className={styles.requirements}>
          <legend className={styles.legend}>{t('requirements')}</legend>
          {requirement('passwordRequireLowercase', t('lowercase'))}
          {requirement('passwordRequireUppercase', t('uppercase'))}
          {requirement('passwordRequireNumeric', t('numeric'))}
          {requirement('passwordRequireSymbol', t('symbol'))}
        </fieldset>
        {editable && (
          <div>
            <Button type="submit" isPending={pending} isDisabled={!changed}>
              {tCommon('save')}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}
