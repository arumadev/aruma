import { useEffect, useState, type FormEvent } from 'react';
import { useI18n } from '../i18n';
import { useSession, useUser } from '../session';
import { createTeam, joinTeam, normalizeCode, watchMyTeams, watchTeam, type MyTeam } from '../data/teams';
import type { Team } from '../data/model';
import { go, href } from '../route';
import { Empty, ErrorText, Loading } from '../components/ui';

function TeamRow({ team }: { team: MyTeam }) {
  const { t } = useI18n();
  const [info, setInfo] = useState<Team | null | undefined>(undefined);
  useEffect(() => watchTeam(team.id, setInfo, () => setInfo(null)), [team.id]);
  return (
    <li>
      <a className="team-row" href={href({ screen: 'team', teamId: team.id, tab: 'tasks' })}>
        <span className="team-name">{info === undefined ? '…' : (info?.name ?? team.id)}</span>
        <span className="badge">{team.role === 'owner' ? t('teams.role.owner') : t('teams.role.member')}</span>
        <code className="code" dir="ltr">{team.id}</code>
      </a>
    </li>
  );
}

export function Teams() {
  const { t } = useI18n();
  const user = useUser();
  const { name } = useSession();
  const [teams, setTeams] = useState<MyTeam[] | null>(null);
  const [listError, setListError] = useState<unknown>(null);
  const [newName, setNewName] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState<'create' | 'join' | null>(null);
  const [createError, setCreateError] = useState<unknown>(null);
  const [joinError, setJoinError] = useState<unknown>(null);

  useEffect(
    () =>
      watchMyTeams(
        user.uid,
        (list) => {
          setTeams(list);
          setListError(null);
        },
        setListError,
      ),
    [user.uid],
  );

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy('create');
    setCreateError(null);
    try {
      const id = await createTeam(newName, user.uid, name);
      go({ screen: 'team', teamId: id, tab: 'tasks' });
    } catch (err) {
      setCreateError(err);
    } finally {
      setBusy(null);
    }
  }

  async function join(e: FormEvent) {
    e.preventDefault();
    const id = normalizeCode(code);
    if (teams?.some((x) => x.id === id)) return go({ screen: 'team', teamId: id, tab: 'tasks' });
    setBusy('join');
    setJoinError(null);
    try {
      go({ screen: 'team', teamId: await joinTeam(id, user.uid, name), tab: 'tasks' });
    } catch (err) {
      setJoinError(err);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="page teams">
      <section className="card" aria-labelledby="my-teams">
        <h1 id="my-teams">{t('teams.title')}</h1>
        {listError ? <ErrorText error={listError} /> : teams === null ? <Loading /> : teams.length === 0 ? (
          <Empty>{t('teams.empty')}</Empty>
        ) : (
          <ul className="team-list" data-testid="team-list">
            {teams.map((x) => <TeamRow key={x.id} team={x} />)}
          </ul>
        )}
      </section>
      <div className="two">
        <section className="card" aria-labelledby="create-team">
          <h2 id="create-team">{t('teams.createTitle')}</h2>
          <form onSubmit={create} className="row-form">
            <label className="field grow">
              <span>{t('teams.createName')}</span>
              <input name="teamName" required maxLength={60} value={newName} onChange={(e) => setNewName(e.target.value)} />
            </label>
            <button className="primary" disabled={busy !== null || !newName.trim()}>{t('teams.createButton')}</button>
          </form>
          <ErrorText error={createError} />
        </section>
        <section className="card" aria-labelledby="join-team">
          <h2 id="join-team">{t('teams.joinTitle')}</h2>
          <form onSubmit={join} className="row-form">
            <label className="field grow">
              <span>{t('teams.joinCode')}</span>
              <input name="code" required dir="ltr" autoCapitalize="characters" autoComplete="off" spellCheck={false}
                maxLength={12} value={code} onChange={(e) => setCode(e.target.value)} />
            </label>
            <button className="primary" disabled={busy !== null || !code.trim()}>{t('teams.joinButton')}</button>
          </form>
          <ErrorText error={joinError} />
        </section>
      </div>
    </div>
  );
}
