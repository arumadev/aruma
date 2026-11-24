import { useEffect, useState } from 'react';
import { useLive } from '../live';
import { useI18n } from '../i18n';
import { useSession, useUser } from '../session';
import { joinTeam, mirrorMembership, watchMembers, watchTeam } from '../data/teams';
import { goOnline } from '../data/presence';
import type { Member, Team as TeamDoc } from '../data/model';
import { isPermissionDenied } from '../errors';
import { href } from '../route';
import { ErrorText, Loading } from '../components/ui';
import { Tasks } from './Tasks';
import { Chat } from './Chat';

export function Team({ teamId, tab }: { teamId: string; tab: 'tasks' | 'chat' }) {
  const { t } = useI18n();
  const user = useUser();
  const { name } = useSession();
  const [mirroredKey, setMirroredKey] = useState<string | null>(null);
  const [presenceError, setPresenceError] = useState<unknown>(null);
  const [joining, setJoining] = useState(false);
  const [joinError, setJoinError] = useState<unknown>(null);
  const [copied, setCopied] = useState(false);
  // Bumped after joining, to listen to the member list again (a denied listener has ended).
  const [joins, setJoins] = useState(0);

  const { value: team, error: teamError } = useLive<TeamDoc | null>(teamId, (n, f) => watchTeam(teamId, n, f));
  // The member list is readable by members only: a denial means "not a member".
  const { value: members, error: membersError } = useLive<Member[]>(`${teamId}/${user.uid}/${joins}`, (n, f) =>
    watchMembers(teamId, n, f),
  );

  const isMember = members?.some((m) => m.uid === user.uid) ?? false;
  const presenceKey = `${teamId}/${user.uid}/${name}`;
  const mirrored = mirroredKey === presenceKey;

  // A member makes sure the Realtime Database knows it (chat and presence rules read it), then
  // shows as online in this team until the screen closes.
  useEffect(() => {
    if (!isMember) return;
    let live = true;
    let offline: (() => void) | undefined;
    mirrorMembership(teamId, user.uid)
      .then(() => {
        if (!live) return;
        setMirroredKey(presenceKey);
        offline = goOnline(teamId, user.uid, name, setPresenceError);
      })
      .catch(setPresenceError);
    return () => {
      live = false;
      offline?.();
    };
  }, [isMember, teamId, user.uid, name, presenceKey]);

  async function join() {
    setJoining(true);
    setJoinError(null);
    try {
      await joinTeam(teamId, user.uid, name);
      setJoins((n) => n + 1);
    } catch (e) {
      setJoinError(e);
    } finally {
      setJoining(false);
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(teamId);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // No clipboard (an insecure origin): the code stays visible to copy by hand.
    }
  }

  let body;
  if (teamError) body = <ErrorText error={teamError} />;
  else if (team === undefined) body = <Loading />;
  else if (team === null) body = <p className="error" role="alert">{t('team.notFound', { code: teamId })}</p>;
  else if (membersError && isPermissionDenied(membersError)) {
    body = (
      <div className="card notice" data-testid="not-member">
        <ErrorText error={membersError} />
        <p>{t('team.notMember')}</p>
        <button className="primary" onClick={() => void join()} disabled={joining}>{t('team.join')}</button>
        <ErrorText error={joinError} />
      </div>
    );
  } else if (membersError) body = <ErrorText error={membersError} />;
  else if (members === undefined || (isMember && !mirrored && !presenceError)) body = <Loading />;
  else if (!isMember) body = <Loading />;
  else {
    body = (
      <>
        <ErrorText error={presenceError} />
        {tab === 'tasks' ? (
          <Tasks teamId={teamId} team={team} members={members} />
        ) : (
          <Chat teamId={teamId} />
        )}
      </>
    );
  }

  return (
    <div className="page team">
      <a className="back link" href={href({ screen: 'teams' })}>
        <span aria-hidden="true" className="flip">←</span> {t('team.back')}
      </a>
      <header className="team-head">
        <h1>{team?.name ?? teamId}</h1>
        <div className="code-box">
          <span className="muted">{t('team.code')}</span>
          <code className="code" dir="ltr" data-testid="team-code">{teamId}</code>
          <button type="button" className="small" onClick={() => void copy()}>
            {copied ? t('team.copied') : t('team.copy')}
          </button>
        </div>
      </header>
      {isMember && (
        <nav className="tabs" aria-label={team?.name ?? teamId}>
          <a href={href({ screen: 'team', teamId, tab: 'tasks' })} aria-current={tab === 'tasks' ? 'page' : undefined}>
            {t('team.tasks')}
          </a>
          <a href={href({ screen: 'team', teamId, tab: 'chat' })} aria-current={tab === 'chat' ? 'page' : undefined}>
            {t('team.chat')}
          </a>
        </nav>
      )}
      {body}
    </div>
  );
}
