'use client';

import { useState } from 'react';
import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { CopyButton, Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import { ApiError } from '@/lib/api/client';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useAuthSection } from './AuthSection';
import styles from './MailboxScreen.module.css';

/** Dev mode only: the emails Auth would have sent, with their action links. */
export function MailboxScreen() {
  const t = useTranslations('auth.mailbox');
  const tCommon = useTranslations('common');
  const tTypes = useTranslations('auth.templates.types');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const { mailbox } = useAuthSection();
  const [clearing, setClearing] = useState(false);
  useDocumentTitle(t('title'));

  // 404: this instance never keeps email (it is not a development setup).
  if (mailbox.error instanceof ApiError && mailbox.error.code === 404) {
    return (
      <EmptyState icon="inbox" title={t('offTitle')}>
        {t('offBody')}
      </EmptyState>
    );
  }
  if (mailbox.error && !mailbox.data) {
    return (
      <Banner tone="error" action={<Button variant="text" onPress={mailbox.reload}>{tCommon('retry')}</Button>}>
        {errorText.message(mailbox.error)}
      </Banner>
    );
  }
  if (!mailbox.data) return <Skeleton lines={4} />;
  const messages = mailbox.data.messages;

  return (
    <>
      {mailbox.data.enabled ? (
        <Banner tone="info" title={t('devTitle')}>
          {t('devBody')}
        </Banner>
      ) : (
        <Banner tone="warning" title={t('smtpTitle')}>
          {t('smtpBody')}
        </Banner>
      )}
      <div className={styles.toolbar}>
        <Button variant="outlined" icon="rotate" isPending={mailbox.loading} onPress={mailbox.reload}>
          {t('refresh')}
        </Button>
        {messages.length > 0 && (
          <Button variant="outlined" icon="delete" onPress={() => setClearing(true)}>
            {t('clear')}
          </Button>
        )}
      </div>
      {messages.length === 0 ? (
        <EmptyState icon="inbox" title={t('emptyTitle')}>
          {t('emptyBody')}
        </EmptyState>
      ) : (
        <Table caption={t('title')} columns={[t('time'), t('to'), t('subject'), t('link')]}>
          {messages.map((m) => (
            <Row key={m.id}>
              <Cell nowrap>
                <Time value={m.createTime} />
              </Cell>
              <Cell>
                <Ltr mono={false}>{m.to}</Ltr>
              </Cell>
              <Cell>
                <span className={styles.subject}>
                  <bdi>{m.subject}</bdi>
                  <span className={styles.type}>{m.type === 'TEST' ? t('test') : tTypes(m.type)}</span>
                </span>
              </Cell>
              <Cell>
                {m.link && /^https?:\/\//.test(m.link) ? (
                  <span className={styles.link}>
                    <Link className={styles.open} href={m.link} target="_blank" rel="noopener noreferrer">
                      {t('open')}
                    </Link>
                    <CopyButton value={m.link} label={t('copyLink')} />
                  </span>
                ) : (
                  '—'
                )}
              </Cell>
            </Row>
          ))}
        </Table>
      )}
      {clearing && (
        <ConfirmDialog
          title={t('clearTitle')}
          confirmLabel={t('clear')}
          danger
          onClose={() => setClearing(false)}
          onConfirm={async () => {
            await api.clearMailbox(project.projectId);
            mailbox.reload();
            notify(t('cleared'));
          }}
        >
          <p>{t('clearBody')}</p>
        </ConfirmDialog>
      )}
    </>
  );
}
