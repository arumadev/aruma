'use client';

import { useState } from 'react';
import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { AuthUser } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDebouncedValue } from '@/lib/hooks/useDebouncedValue';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import { CreateUserDialog } from './CreateUserDialog';
import { ProviderNames } from './ProviderNames';
import { providerLabels } from './providers';
import styles from './UsersScreen.module.css';

export const USERS_PAGE_SIZE = 25;

/** The project's users: search by email prefix or exact uid, a page at a time. */
export function UsersScreen() {
  const t = useTranslations('auth.users');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const [search, setSearch] = useState('');
  const query = useDebouncedValue(search.trim());
  // Page tokens of the pages before the current one; the last is the current page's token.
  const [tokens, setTokens] = useState<{ query: string; stack: string[] }>({ query: '', stack: [] });
  const stack = tokens.query === query ? tokens.stack : [];
  const pageToken = stack.at(-1) ?? '';
  const users = useResource(
    (signal) => api.listAuthUsers(project.projectId, { pageSize: USERS_PAGE_SIZE, pageToken, query }, signal),
    [project.projectId, query, pageToken],
  );
  const [creating, setCreating] = useState(false);
  const editable = project.state === 'ACTIVE';
  useDocumentTitle(t('title'));

  const list = users.data?.users ?? [];
  const total = users.data?.totalSize ?? 0;
  const first = stack.length * USERS_PAGE_SIZE + 1;

  return (
    <>
      <div className={styles.toolbar}>
        <TextField
          className={styles.search}
          type="search"
          label={t('search')}
          placeholder={t('searchHint')}
          value={search}
          onChange={setSearch}
        />
        {editable && (
          <Button icon="add" onPress={() => setCreating(true)}>
            {t('create')}
          </Button>
        )}
      </div>
      {users.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={users.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(users.error)}
        </Banner>
      ) : users.loading && !users.data ? (
        <Skeleton lines={5} />
      ) : list.length === 0 ? (
        query ? (
          <EmptyState icon="search" title={t('noMatchTitle')}>
            {t('noMatchBody')}
          </EmptyState>
        ) : (
          <EmptyState
            icon="person"
            title={t('emptyTitle')}
            action={
              editable && (
                <Button icon="add" onPress={() => setCreating(true)}>
                  {t('create')}
                </Button>
              )
            }
          >
            {t('emptyBody')}
          </EmptyState>
        )
      ) : (
        <>
          <Table caption={t('title')} columns={[t('identifier'), t('providers'), t('created'), t('signedIn'), t('uid')]}>
            {list.map((user) => (
              <UserRow key={user.uid} projectId={project.projectId} user={user} />
            ))}
          </Table>
          <nav className={styles.pager} aria-label={t('pages')}>
            <span className={styles.range} aria-live="polite">
              {t('range', { first, last: first + list.length - 1, total })}
            </span>
            <Button
              variant="text"
              isDisabled={stack.length === 0 || users.loading}
              onPress={() => setTokens({ query, stack: stack.slice(0, -1) })}
            >
              {t('previous')}
            </Button>
            <Button
              variant="text"
              isDisabled={!users.data?.nextPageToken || users.loading}
              onPress={() => setTokens({ query, stack: [...stack, users.data?.nextPageToken ?? ''] })}
            >
              {t('next')}
            </Button>
          </nav>
        </>
      )}
      {creating && <CreateUserDialog projectId={project.projectId} onCreated={users.reload} onClose={() => setCreating(false)} />}
    </>
  );
}

function UserRow({ projectId, user }: { projectId: string; user: AuthUser }) {
  const t = useTranslations('auth.users');
  const tProvider = useTranslations('auth.provider');
  const labels = providerLabels(user).map((l) => tProvider(l));
  return (
    <Row>
      <Cell>
        <span className={styles.identity}>
          <Link className={styles.link} href={routes.authUser(projectId, user.uid)}>
            {user.email ? <Ltr mono={false}>{user.email}</Ltr> : t('anonymousUser')}
          </Link>
          {user.disabled && <Badge tone="error">{t('disabled')}</Badge>}
        </span>
      </Cell>
      <Cell nowrap>
        <ProviderNames names={labels} />
      </Cell>
      <Cell nowrap>
        <Time value={user.createTime} withTime={false} />
      </Cell>
      <Cell nowrap>{user.lastSignInTime ? <Time value={user.lastSignInTime} withTime={false} /> : '—'}</Cell>
      <Cell>
        <Ltr>{user.uid}</Ltr>
      </Cell>
    </Row>
  );
}
