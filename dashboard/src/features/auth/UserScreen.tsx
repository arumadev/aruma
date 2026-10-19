'use client';

import { useRouter, useSearchParams } from 'next/navigation';
import { useId, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button, LinkButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { CopyButton, Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { AuthUser, Locale } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { useLocale } from '@/lib/i18n/LocaleProvider';
import { LOCALES } from '@/lib/i18n/locales';
import { routes } from '@/lib/routes';
import { checkClaims, CLAIMS_MAX_BYTES, formatClaims } from './claims';
import { ProviderNames } from './ProviderNames';
import { providerLabel, providerLabels } from './providers';
import styles from './UserScreen.module.css';

type Action = 'disable' | 'revoke' | 'delete' | 'reset';

/** One user: identifiers, sign-in methods and metadata, account actions and custom claims. */
export function UserScreen() {
  const t = useTranslations('auth.user');
  const errorText = useErrorText();
  const { project } = useProject();
  const uid = useSearchParams().get('uid') ?? '';
  const user = useResource((signal) => api.getAuthUser(project.projectId, uid, signal), [project.projectId, uid], uid !== '');
  useDocumentTitle(user.data?.email || uid || t('title'));

  const back = (
    <div>
      <LinkButton variant="text" icon="arrowBack" href={routes.authUsers(project.projectId)}>
        {t('back')}
      </LinkButton>
    </div>
  );
  if (user.error && !user.data) {
    return (
      <>
        {back}
        <Banner tone="error">{errorText.message(user.error)}</Banner>
      </>
    );
  }
  if (!user.data) return <Skeleton lines={3} blocks={2} />;

  return (
    <>
      {back}
      <UserDetails user={user.data} onChange={user.setData} />
    </>
  );
}

function UserDetails({ user, onChange }: { user: AuthUser; onChange: (user: AuthUser) => void }) {
  const t = useTranslations('auth.user');
  const tProvider = useTranslations('auth.provider');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const router = useRouter();
  const { project } = useProject();
  const id = project.projectId;
  const editable = project.state === 'ACTIVE';
  const [action, setAction] = useState<Action | null>(null);
  const [enabling, setEnabling] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [resetLink, setResetLink] = useState<string | null>(null);

  async function enable() {
    setEnabling(true);
    setError(null);
    try {
      onChange(await api.updateAuthUser(id, user.uid, { disabled: false }));
      notify(t('enabled'));
    } catch (err) {
      setError(err);
    } finally {
      setEnabling(false);
    }
  }

  const labels = providerLabels(user).map((l) => tProvider(l));

  return (
    <>
      <Card
        title={user.email || t('anonymous')}
        description={user.displayName ? <bdi>{user.displayName}</bdi> : undefined}
        actions={
          editable && (
            <>
              {user.email && (
                <Button variant="outlined" icon="send" onPress={() => setAction('reset')}>
                  {t('sendReset')}
                </Button>
              )}
              <Button variant="outlined" icon="logout" onPress={() => setAction('revoke')}>
                {t('revoke')}
              </Button>
              {user.disabled ? (
                <Button variant="outlined" icon="checkCircle" isPending={enabling} onPress={() => void enable()}>
                  {t('enable')}
                </Button>
              ) : (
                <Button variant="outlined" icon="block" onPress={() => setAction('disable')}>
                  {t('disable')}
                </Button>
              )}
              <Button variant="danger" icon="delete" onPress={() => setAction('delete')}>
                {t('delete')}
              </Button>
            </>
          )
        }
      >
        <div className={styles.badges}>
          {user.disabled ? <Badge tone="error">{t('disabled')}</Badge> : <Badge tone="primary">{t('active')}</Badge>}
          {user.email && (user.emailVerified ? <Badge>{t('verified')}</Badge> : <Badge tone="warning">{t('unverified')}</Badge>)}
        </div>
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
        {resetLink && (
          <Banner tone="info" title={t('resetLinkTitle')} action={<CopyButton value={resetLink} label={t('copyResetLink')} />}>
            <p className={styles.link}>
              <Ltr>{resetLink}</Ltr>
            </p>
          </Banner>
        )}
        <DetailList>
          <Detail label={t('uid')} value={user.uid} copyLabel={t('copyUid')} />
          {user.email && <Detail label={t('email')} value={user.email} copyLabel={t('copyEmail')} />}
          <Detail label={t('providers')}>
            <ProviderNames names={labels} />
          </Detail>
          <Detail label={t('password')}>{user.hasPassword ? t('passwordSet') : t('passwordNone')}</Detail>
          <Detail label={t('created')}>
            <Time value={user.createTime} />
          </Detail>
          <Detail label={t('signedIn')}>{user.lastSignInTime ? <Time value={user.lastSignInTime} /> : t('never')}</Detail>
          <Detail label={t('refreshed')}>{user.lastRefreshTime ? <Time value={user.lastRefreshTime} /> : t('never')}</Detail>
          <Detail label={t('validSince')}>
            <Time value={user.tokensValidAfterTime} />
          </Detail>
        </DetailList>
      </Card>
      <Card title={t('methods')} description={t('methodsHint')}>
        {user.providers.length === 0 ? (
          <p className={styles.muted}>{t('noMethods')}</p>
        ) : (
          <Table caption={t('methods')} columns={[t('method'), t('methodEmail')]}>
            {user.providers.map((p) => {
              const label = providerLabel(p.providerId);
              return (
                <Row key={p.providerId}>
                  <Cell nowrap>{label ? tProvider(label) : <Ltr>{p.providerId}</Ltr>}</Cell>
                  <Cell>{p.email ? <Ltr mono={false}>{p.email}</Ltr> : '—'}</Cell>
                </Row>
              );
            })}
          </Table>
        )}
      </Card>
      <ClaimsCard key={user.customClaims ?? ''} user={user} editable={editable} onSaved={onChange} />
      {action === 'reset' && (
        <ResetDialog
          user={user}
          onClose={() => setAction(null)}
          onSent={(link) => {
            setResetLink(link);
            notify(t('resetSent', { email: user.email ?? '' }));
          }}
        />
      )}
      {action === 'disable' && (
        <ConfirmDialog
          title={t('disableTitle')}
          confirmLabel={t('disable')}
          danger
          onClose={() => setAction(null)}
          onConfirm={async () => {
            onChange(await api.updateAuthUser(id, user.uid, { disabled: true }));
            notify(t('disabledDone'));
          }}
        >
          <p>{t('disableBody')}</p>
        </ConfirmDialog>
      )}
      {action === 'revoke' && (
        <ConfirmDialog
          title={t('revokeTitle')}
          confirmLabel={t('revoke')}
          onClose={() => setAction(null)}
          onConfirm={async () => {
            onChange(await api.revokeAuthUserSessions(id, user.uid));
            notify(t('revoked'));
          }}
        >
          <p>{t('revokeBody')}</p>
        </ConfirmDialog>
      )}
      {action === 'delete' && (
        <ConfirmDialog
          title={t('deleteTitle')}
          confirmLabel={t('delete')}
          danger
          onClose={() => setAction(null)}
          onConfirm={async () => {
            await api.deleteAuthUser(id, user.uid);
            notify(t('deleted'));
            router.push(routes.authUsers(id));
          }}
        >
          <p>{t('deleteBody')}</p>
          <p>
            <Ltr>{user.email || user.uid}</Ltr>
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}

/** Asks which language the reset email is written in; the user's own is unknown to the console. */
function ResetDialog({ user, onClose, onSent }: { user: AuthUser; onClose: () => void; onSent: (link: string) => void }) {
  const t = useTranslations('auth.user');
  const tLocale = useTranslations('locale');
  const { locale } = useLocale();
  const { project } = useProject();
  const [language, setLanguage] = useState<Locale>(locale);
  return (
    <ConfirmDialog
      title={t('resetTitle')}
      confirmLabel={t('sendReset')}
      onClose={onClose}
      onConfirm={async () => onSent((await api.sendPasswordReset(project.projectId, user.uid, language)).link)}
    >
      <p>{t('resetBody', { email: user.email ?? '' })}</p>
      <Select
        label={t('resetLanguage')}
        value={language}
        onChange={(v) => setLanguage(v as Locale)}
        options={LOCALES.map((l) => ({ id: l, label: tLocale(l) }))}
      />
    </ConfirmDialog>
  );
}

/** Custom claims as JSON, checked as the user types against the server's rules. */
function ClaimsCard({ user, editable, onSaved }: { user: AuthUser; editable: boolean; onSaved: (user: AuthUser) => void }) {
  const t = useTranslations('auth.claims');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const counterId = useId();
  const [text, setText] = useState(() => formatClaims(user.customClaims));
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const check = checkClaims(text);
  const saved = checkClaims(formatClaims(user.customClaims)).compact;
  const changed = check.compact !== saved;

  async function save() {
    if (check.problem || !changed) return;
    setPending(true);
    setError(null);
    try {
      onSaved(await api.updateAuthUser(project.projectId, user.uid, { customClaims: check.compact }));
      notify(t('saved'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  const problem =
    check.problem === 'reserved' ? t('reserved', { name: check.reserved ?? '' }) : check.problem ? t(`problem.${check.problem}`) : undefined;
  return (
    <Card title={t('title')} description={t('description')}>
      <form
        className={styles.form}
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
        <TextField
          label={t('label')}
          multiline
          rows={6}
          ltr
          spellCheck="false"
          placeholder='{"role": "admin"}'
          value={text}
          onChange={setText}
          isReadOnly={!editable}
          errorMessage={problem}
          aria-describedby={counterId}
        />
        <p id={counterId} className={check.bytes > CLAIMS_MAX_BYTES ? `${styles.counter} ${styles.over}` : styles.counter}>
          {t('counter', { used: check.bytes, max: CLAIMS_MAX_BYTES })}
        </p>
        {editable && (
          <div>
            <Button type="submit" isPending={pending} isDisabled={!changed || Boolean(check.problem)}>
              {tCommon('save')}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}
