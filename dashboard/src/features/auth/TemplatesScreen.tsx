'use client';

import { useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Dialog } from '@/components/ui/Dialog';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { AuthConfig, EmailTemplate, EmailTemplateType, Locale, SmtpSettings, SmtpTls, SmtpUpdate } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource, type Resource } from '@/lib/hooks/useResource';
import { LOCALES } from '@/lib/i18n/locales';
import { useAuthSection } from './AuthSection';
import { SettingSwitch } from './SettingSwitch';
import { bodyText, fillVariables, TEMPLATE_VARIABLES } from './templatePreview';
import styles from './TemplatesScreen.module.css';

// A subject longer than this is refused (INVALID_TEMPLATE).
const SUBJECT_MAX = 200;

export const TEMPLATE_TYPES: EmailTemplateType[] = ['VERIFY_EMAIL', 'PASSWORD_RESET', 'VERIFY_AND_CHANGE_EMAIL', 'RECOVER_EMAIL'];

/** The emails Auth sends, per type and language, and the SMTP server that sends them. */
export function TemplatesScreen() {
  const t = useTranslations('auth.templates');
  const { project } = useProject();
  const config = useResource((signal) => api.getAuthConfig(project.projectId, signal), [project.projectId]);
  useDocumentTitle(t('title'));
  return (
    <>
      <TemplateEditor appName={config.data?.appName || project.displayName} />
      <AppNameCard config={config} />
      <SmtpCard />
    </>
  );
}

function TemplateEditor({ appName }: { appName: string }) {
  const t = useTranslations('auth.templates');
  const tCommon = useTranslations('common');
  const tLocale = useTranslations('locale');
  const errorText = useErrorText();
  const { project } = useProject();
  const templates = useResource((signal) => api.listEmailTemplates(project.projectId, signal), [project.projectId]);
  const [type, setType] = useState<EmailTemplateType>('VERIFY_EMAIL');
  const [language, setLanguage] = useState<Locale>('en');

  let content;
  if (templates.error) {
    content = (
      <Banner tone="error" action={<Button variant="text" onPress={templates.reload}>{tCommon('retry')}</Button>}>
        {errorText.message(templates.error)}
      </Banner>
    );
  } else if (!templates.data) {
    content = <Skeleton lines={4} />;
  } else {
    const template = templates.data.templates.find((x) => x.type === type && x.language === language);
    content = template ? (
      <TemplateForm
        key={`${type}/${language}/${template.subject}/${template.body}`}
        template={template}
        appName={appName}
        onSaved={(saved) =>
          templates.setData((current) => ({
            templates: (current?.templates ?? []).map((x) => (x.type === saved.type && x.language === saved.language ? saved : x)),
          }))
        }
      />
    ) : (
      <Banner tone="error">{t('missing')}</Banner>
    );
  }

  return (
    <Card title={t('title')} description={t('description')}>
      {templates.data && (
        <div className={styles.pickers}>
          <Select
            className={styles.picker}
            label={t('type')}
            value={type}
            onChange={(v) => setType(v as EmailTemplateType)}
            options={TEMPLATE_TYPES.map((x) => ({ id: x, label: t(`types.${x}`) }))}
          />
          <Select
            className={styles.picker}
            label={t('language')}
            value={language}
            onChange={(v) => setLanguage(v as Locale)}
            options={LOCALES.map((l) => ({ id: l, label: tLocale(l) }))}
          />
        </div>
      )}
      {content}
    </Card>
  );
}

function TemplateForm({
  template,
  appName,
  onSaved,
}: {
  template: EmailTemplate;
  appName: string;
  onSaved: (t: EmailTemplate) => void;
}) {
  const t = useTranslations('auth.templates');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const [subject, setSubject] = useState(template.subject);
  const [body, setBody] = useState(template.body);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [resetting, setResetting] = useState(false);
  const changed = subject !== template.subject || body !== template.body;
  const dir = template.language === 'ar' ? 'rtl' : 'ltr';
  // The server's rules (INVALID_TEMPLATE), checked as you type.
  const subjectProblem = !subject.trim() ? t('subjectRequired') : subject.length > SUBJECT_MAX ? t('subjectTooLong', { max: SUBJECT_MAX }) : undefined;
  const bodyProblem = !body.includes('%LINK%') ? t('needsLink') : undefined;

  async function save(event: FormEvent) {
    event.preventDefault();
    if (subjectProblem || bodyProblem) return;
    setPending(true);
    setError(null);
    try {
      onSaved(await api.saveEmailTemplate(project.projectId, template.type, template.language, { subject, body }));
      notify(t('saved'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  return (
    <div className={styles.editor}>
      <form className={styles.form} onSubmit={save} noValidate>
        <div className={styles.status}>
          {template.customized ? <Badge tone="primary">{t('customized')}</Badge> : <Badge>{t('default')}</Badge>}
        </div>
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
        <TextField
          label={t('subject')}
          textDir={dir}
          lang={template.language}
          value={subject}
          onChange={setSubject}
          isReadOnly={!editable}
          errorMessage={subjectProblem}
        />
        <TextField
          label={t('body')}
          multiline
          rows={12}
          ltr
          spellCheck="false"
          lang={template.language}
          value={body}
          onChange={setBody}
          isReadOnly={!editable}
          errorMessage={bodyProblem}
          description={
            <span className={styles.variables}>
              {t('variables')}
              {TEMPLATE_VARIABLES.map((v) => (
                <Ltr key={v}>{v}</Ltr>
              ))}
            </span>
          }
        />
        {editable && (
          <div className={styles.actions}>
            <Button type="submit" isPending={pending} isDisabled={!changed || Boolean(subjectProblem || bodyProblem)}>
              {tCommon('save')}
            </Button>
            {template.customized && (
              <Button variant="outlined" icon="undo" onPress={() => setResetting(true)}>
                {t('reset')}
              </Button>
            )}
          </div>
        )}
      </form>
      <Preview language={template.language} appName={appName} subject={subject} body={body} />
      {resetting && (
        <ConfirmDialog
          title={t('resetTitle')}
          confirmLabel={t('reset')}
          onClose={() => setResetting(false)}
          onConfirm={async () => {
            onSaved(await api.resetEmailTemplate(project.projectId, template.type, template.language));
            notify(t('resetDone'));
          }}
        >
          <p>{t('resetBody')}</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

/** The email as a recipient would read it, in the template's language and direction. */
function Preview({ language, appName, subject, body }: { language: Locale; appName: string; subject: string; body: string }) {
  const t = useTranslations('auth.templates');
  const { project } = useProject();
  // Links open on the project's auth domain, or on this server while the project uses the shared one.
  const origin = typeof window !== 'undefined' && window.location.hostname === project.authDomain ? window.location.origin : `https://${project.authDomain}`;
  const link = `${origin}/__/auth/action?mode=verifyEmail&oobCode=…&lang=${language}`;
  const values = {
    '%APP_NAME%': appName,
    '%DISPLAY_NAME%': language === 'ar' ? 'سارة' : 'Sarah',
    '%EMAIL%': 'sarah@example.com',
    '%NEW_EMAIL%': 'sarah.new@example.com',
    '%LINK%': link,
  };
  const text = fillVariables(bodyText(body), values);
  return (
    <section className={styles.preview} aria-label={t('preview')}>
      <h3 className={styles.previewLabel}>{t('preview')}</h3>
      <div className={styles.mail} lang={language} dir={language === 'ar' ? 'rtl' : 'ltr'}>
        <p className={styles.mailSubject}>{fillVariables(subject, values)}</p>
        <p className={styles.mailBody}>
          {text.split(link).map((part, i) => (
            <span key={i}>
              {i > 0 && (
                <bdi dir="ltr" className={styles.mailLink}>
                  {link}
                </bdi>
              )}
              {part}
            </span>
          ))}
        </p>
      </div>
    </section>
  );
}

/** %APP_NAME% in every email. */
function AppNameCard({ config }: { config: Resource<AuthConfig> }) {
  const t = useTranslations('auth.appName');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const [name, setName] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const saved = config.data?.appName ?? '';
  const value = name ?? saved;

  async function save(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      config.setData(await api.updateAuthConfig(project.projectId, { appName: value.trim() }));
      setName(null);
      notify(t('saved'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  return (
    <Card title={t('title')} description={t('description')}>
      <form className={`${styles.form} ${styles.narrow}`} onSubmit={save} noValidate>
        {Boolean(error || config.error) && <Banner tone="error">{errorText.message(error || config.error)}</Banner>}
        <TextField label={t('label')} value={value} onChange={setName} maxLength={256} isDisabled={!config.data} placeholder={project.displayName} />
        {project.state === 'ACTIVE' && (
          <div>
            <Button type="submit" isPending={pending} isDisabled={!config.data || value.trim() === saved}>
              {tCommon('save')}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}

function SmtpCard() {
  const t = useTranslations('auth.smtp');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const smtp = useResource((signal) => api.getSmtp(project.projectId, signal), [project.projectId]);

  let content;
  if (smtp.error) {
    content = (
      <Banner tone="error" action={<Button variant="text" onPress={smtp.reload}>{tCommon('retry')}</Button>}>
        {errorText.message(smtp.error)}
      </Banner>
    );
  } else if (!smtp.data) content = <Skeleton lines={3} />;
  else content = <SmtpForm key={JSON.stringify(smtp.data)} settings={smtp.data} onSaved={smtp.setData} />;

  return (
    <Card title={t('title')} description={t('description')}>
      {content}
    </Card>
  );
}

function SmtpForm({ settings, onSaved }: { settings: SmtpSettings; onSaved: (s: SmtpSettings) => void }) {
  const t = useTranslations('auth.smtp');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const { mailbox } = useAuthSection();
  const editable = project.state === 'ACTIVE';
  // The dev mailbox is a choice only where the instance allows it (its route answers).
  const mailboxAllowed = Boolean(mailbox.data);
  const [devMailbox, setDevMailbox] = useState(settings.devMailbox);
  const [draft, setDraft] = useState({
    host: settings.host,
    port: String(settings.port),
    tls: settings.tls,
    username: settings.username,
    senderName: settings.senderName,
    senderAddress: settings.senderAddress,
    replyTo: settings.replyTo,
  });
  // Write-only: empty keeps the saved password; removing it sends an empty one.
  const [password, setPassword] = useState('');
  const [removing, setRemoving] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [testing, setTesting] = useState(false);
  const port = Number(draft.port);
  const portInvalid = !Number.isInteger(port) || port < 1 || port > 65535;

  async function patch(body: SmtpUpdate, done: string) {
    setPending(true);
    setError(null);
    try {
      onSaved(await api.updateSmtp(project.projectId, body));
      setPassword('');
      // Saving can switch between SMTP and the dev mailbox, which the Mailbox tab follows.
      mailbox.reload();
      notify(done);
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    if (portInvalid) return;
    // devMailbox is sent only when changed: sending it pins the project's choice.
    await patch(
      {
        host: draft.host.trim(),
        port,
        tls: draft.tls,
      username: draft.username.trim(),
      senderName: draft.senderName.trim(),
      senderAddress: draft.senderAddress.trim(),
        replyTo: draft.replyTo.trim(),
        ...(password && { password }),
        ...(devMailbox !== settings.devMailbox && { devMailbox }),
      },
      t('saved'),
    );
  }

  const text = (name: keyof typeof draft, label: string, extra: { ltr?: boolean; type?: string; description?: string } = {}) => (
    <TextField
      label={label}
      ltr={extra.ltr ?? true}
      type={extra.type}
      description={extra.description}
      value={draft[name]}
      onChange={(v) => setDraft((d) => ({ ...d, [name]: v }))}
      isReadOnly={!editable}
    />
  );

  // The test dialog stays outside the form: its own submit would bubble through the portal.
  return (
    <>
      <form className={styles.form} onSubmit={save} noValidate>
        {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
        {settings.devMailbox && <Banner tone="info">{t('devMailboxOn')}</Banner>}
        {mailboxAllowed && (
          <SettingSwitch label={t('devMailbox')} description={t('devMailboxHint')} isSelected={devMailbox} onChange={setDevMailbox} isDisabled={!editable} />
        )}
        <div className={styles.grid}>
          {text('host', t('host'))}
          <TextField
            label={t('port')}
            ltr
            inputMode="numeric"
            value={draft.port}
            onChange={(v) => setDraft((d) => ({ ...d, port: v }))}
            isReadOnly={!editable}
            errorMessage={portInvalid ? t('portInvalid') : undefined}
          />
          <Select
            label={t('security')}
            value={draft.tls}
            onChange={(v) => setDraft((d) => ({ ...d, tls: v as SmtpTls }))}
            options={(['starttls', 'tls', 'none'] as const).map((s) => ({ id: s, label: t(`securityOptions.${s}`) }))}
            isDisabled={!editable}
          />
          {text('username', t('username'))}
          <TextField
            label={t('password')}
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={setPassword}
            isReadOnly={!editable}
            description={settings.passwordSet ? t('passwordSaved') : t('passwordMissing')}
          />
          {text('senderName', t('senderName'), { ltr: false })}
          {text('senderAddress', t('senderAddress'), { type: 'email' })}
          {text('replyTo', t('replyTo'), { type: 'email', description: tCommon('optional') })}
        </div>
        {editable && (
          <div className={styles.actions}>
            <Button type="submit" isPending={pending}>
              {tCommon('save')}
            </Button>
            <Button variant="outlined" icon="send" onPress={() => setTesting(true)}>
              {t('test')}
            </Button>
            {settings.passwordSet && (
              <Button variant="text" icon="delete" onPress={() => setRemoving(true)}>
                {t('removePassword')}
              </Button>
            )}
          </div>
        )}
      </form>
      {testing && <TestDialog onClose={() => setTesting(false)} />}
      {removing && (
        <ConfirmDialog
          title={t('removePasswordTitle')}
          confirmLabel={t('removePassword')}
          danger
          onClose={() => setRemoving(false)}
          onConfirm={() => patch({ password: '' }, t('passwordRemoved'))}
        >
          <p>{t('removePasswordBody')}</p>
        </ConfirmDialog>
      )}
    </>
  );
}

function TestDialog({ onClose }: { onClose: () => void }) {
  const t = useTranslations('auth.smtp');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const { mailbox } = useAuthSection();
  const [to, setTo] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function send() {
    if (!to.trim()) return;
    setPending(true);
    setError(null);
    try {
      const { devMailbox } = await api.testSmtp(project.projectId, to.trim());
      if (devMailbox) mailbox.reload();
      notify(devMailbox ? t('testInMailbox') : t('testSent', { email: to.trim() }));
      onClose();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  return (
    <Dialog
      isOpen
      onOpenChange={(open) => !open && onClose()}
      title={t('testTitle')}
      isBusy={pending}
      onSubmit={send}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" icon="send" isPending={pending} isDisabled={!to.trim()}>
            {t('testSend')}
          </Button>
        </>
      }
    >
      <p>{t('testBody')}</p>
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
      <TextField label={t('testTo')} type="email" ltr autoFocus value={to} onChange={setTo} />
    </Dialog>
  );
}
