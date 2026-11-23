import { useState, type FormEvent } from 'react';
import { useI18n, type Lang } from '../i18n';
import { useTheme, type Theme } from '../theme';
import { useSession, useUser } from '../session';
import { rename, signOut, uploadAvatar } from '../data/profile';
import { app, usingEmulators } from '../firebase';
import { limits } from '../data/model';
import { go } from '../route';
import { Avatar, ErrorText, Progress } from '../components/ui';

export function Profile() {
  const { t, lang, setLang, nameOf } = useI18n();
  const { theme, setTheme } = useTheme();
  const user = useUser();
  const { profile, name } = useSession();
  // null follows the current name (changed in another tab or in the Flutter app) until the user types.
  const [edited, setDraft] = useState<string | null>(null);
  const draft = edited ?? name;
  const [saved, setSaved] = useState(false);
  const [nameError, setNameError] = useState<unknown>(null);
  const [upload, setUpload] = useState<number | null>(null);
  const [photoError, setPhotoError] = useState<unknown>(null);

  async function save(e: FormEvent) {
    e.preventDefault();
    setNameError(null);
    setSaved(false);
    try {
      await rename(user, draft);
      setDraft(null);
      setSaved(true);
    } catch (err) {
      setNameError(err);
    }
  }

  async function pick(file: File | undefined) {
    if (!file) return;
    setPhotoError(null);
    setUpload(0);
    try {
      await uploadAvatar(user, name, file, setUpload);
    } catch (err) {
      setPhotoError(err);
    } finally {
      setUpload(null);
    }
  }

  return (
    <div className="page profile">
      <section className="card" aria-labelledby="profile-title">
        <h1 id="profile-title">{t('profile.title')}</h1>
        <div className="profile-head">
          <Avatar uid={user.uid} name={name} photoURL={profile?.photoURL} size={72} />
          <div className="stack-tight">
            <strong className="profile-name">{name}</strong>
            <span className="muted" dir="ltr">{user.isAnonymous ? '' : user.email}</span>
          </div>
        </div>
        <div className="stack">
          <div className="field">
            <span>{t('profile.picture')}</span>
            <label className="button small file-button">
              {t('profile.change')}
              <input type="file" accept="image/*" className="visually-hidden" disabled={upload !== null}
                onChange={(e) => {
                  void pick(e.target.files?.[0]);
                  e.target.value = '';
                }} />
            </label>
            <small className="muted">{t('profile.pictureHint')}</small>
            {upload !== null && <Progress value={upload} label={t('tasks.uploading', { percent: Math.round(upload * 100) })} />}
            <ErrorText error={photoError} />
          </div>
          <form onSubmit={save} className="row-form">
            <label className="field grow">
              <span>{t('profile.name')}</span>
              <input name="displayName" maxLength={limits.name} value={draft}
                onChange={(e) => { setDraft(e.target.value); setSaved(false); }} />
            </label>
            <button className="primary" disabled={draft.trim() === name}>{t('common.save')}</button>
          </form>
          {saved && <p className="ok" role="status">{t('common.saved')}</p>}
          <ErrorText error={nameError} />
          <div className="two">
            <label className="field">
              <span>{t('language.label')}</span>
              <select name="language" value={lang} onChange={(e) => setLang(e.target.value as Lang)}>
                <option value="en" lang="en">{nameOf('en')}</option>
                <option value="ar" lang="ar">{nameOf('ar')}</option>
              </select>
            </label>
            <label className="field">
              <span>{t('theme.label')}</span>
              <select name="theme" value={theme} onChange={(e) => setTheme(e.target.value as Theme)}>
                <option value="system">{t('theme.system')}</option>
                <option value="light">{t('theme.light')}</option>
                <option value="dark">{t('theme.dark')}</option>
              </select>
            </label>
          </div>
        </div>
      </section>
      <section className="card" aria-labelledby="account-title">
        <h2 id="account-title">{t('profile.account')}</h2>
        {user.isAnonymous && <p className="notice-text">{t('profile.guest')}</p>}
        <p className="muted small-text">
          {usingEmulators ? t('profile.emulators') : t('profile.project', { id: app.options.projectId ?? '' })}
        </p>
        <button className="danger" onClick={() => {
          go({ screen: 'teams' });
          void signOut();
        }}>{t('profile.signOut')}</button>
      </section>
    </div>
  );
}
