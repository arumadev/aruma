'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { Time } from '@/components/ui/Time';
import * as api from '@/lib/api/console';
import type { Session } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import styles from './AccountScreen.module.css';
import { describeUserAgent } from './userAgent';

/** The account's signed-in browsers; any but this one can be signed out. */
export function SessionsCard() {
  const t = useTranslations('account');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const sessions = useResource((signal) => api.listSessions(signal), []);
  const [revoking, setRevoking] = useState<Session | null>(null);

  const device = (s: Session) => {
    const { browser, system } = describeUserAgent(s.userAgent);
    if (browser && system) return t('deviceOn', { browser, system });
    return browser ?? system ?? t('unknownDevice');
  };

  const list = sessions.data?.sessions ?? [];
  return (
    <Card title={t('sessions')} description={t('sessionsHint')}>
      {sessions.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={sessions.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(sessions.error)}
        </Banner>
      ) : sessions.loading && !sessions.data ? (
        <Skeleton lines={2} />
      ) : (
        <Table caption={t('sessions')} columns={[t('device'), t('ip'), t('lastSeen'), t('signedIn'), t('actions')]}>
          {list.map((s) => (
            <Row key={s.id}>
              <Cell>
                <span className={styles.device}>
                  {device(s)}
                  {s.current && <Badge tone="primary">{t('thisDevice')}</Badge>}
                </span>
              </Cell>
              <Cell nowrap>{s.ip ? <Ltr>{s.ip}</Ltr> : '—'}</Cell>
              <Cell nowrap>
                <Time value={s.lastSeenTime} />
              </Cell>
              <Cell nowrap>
                <Time value={s.createTime} />
              </Cell>
              <Cell actions>
                {!s.current && (
                  <Button variant="text" onPress={() => setRevoking(s)}>
                    {t('signOutSession')}
                  </Button>
                )}
              </Cell>
            </Row>
          ))}
        </Table>
      )}
      {revoking && (
        <ConfirmDialog
          title={t('revokeTitle')}
          confirmLabel={t('signOutSession')}
          danger
          onClose={() => setRevoking(null)}
          onConfirm={async () => {
            await api.revokeSession(revoking.id);
            sessions.setData((current) => ({ sessions: (current?.sessions ?? []).filter((s) => s.id !== revoking.id) }));
            notify(t('revoked'));
          }}
        >
          <p>{t('revokeBody', { device: device(revoking) })}</p>
        </ConfirmDialog>
      )}
    </Card>
  );
}
