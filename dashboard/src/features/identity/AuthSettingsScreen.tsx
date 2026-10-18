'use client';

import { useState } from 'react';
import { useFormatter, useTranslations } from 'use-intl';
import { Badge, type BadgeTone } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { Cell, Row, Table } from '@/components/ui/Table';
import { TextField } from '@/components/ui/TextField';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { AuthLimits, SigningKey, SigningKeyPurpose } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import styles from './AuthSettingsScreen.module.css';
import { TEMPLATE_TYPES } from './TemplatesScreen';

const PURPOSES: SigningKeyPurpose[] = ['id_token', 'session_cookie'];
const STATE_TONE: Record<SigningKey['state'], BadgeTone> = { active: 'primary', next: 'neutral', retired: 'warning' };

/** Signing keys and their rotation, and the limits no setting changes. */
export function AuthSettingsScreen() {
  const t = useTranslations('auth.settings');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const keys = useResource((signal) => api.listSigningKeys(project.projectId, signal), [project.projectId]);
  const config = useResource((signal) => api.getAuthConfig(project.projectId, signal), [project.projectId]);
  useDocumentTitle(t('title'));

  return (
    <>
      {keys.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={keys.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(keys.error)}
        </Banner>
      ) : !keys.data ? (
        <Skeleton lines={3} blocks={1} />
      ) : (
        PURPOSES.map((purpose) => (
          <KeysCard
            key={purpose}
            purpose={purpose}
            keys={keys.data?.keys.filter((k) => k.purpose === purpose) ?? []}
            rotationDays={config.data?.limits.signingKeyRotationDays}
            onRotated={(list) => keys.setData({ keys: list })}
          />
        ))
      )}
      {config.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={config.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(config.error)}
        </Banner>
      ) : config.data ? (
        <LimitsCard limits={config.data.limits} />
      ) : (
        <Skeleton lines={3} />
      )}
    </>
  );
}

function KeysCard({
  purpose,
  keys,
  rotationDays,
  onRotated,
}: {
  purpose: SigningKeyPurpose;
  keys: SigningKey[];
  rotationDays?: number;
  onRotated: (keys: SigningKey[]) => void;
}) {
  const t = useTranslations('auth.settings');
  const notify = useSnackbar();
  const { project } = useProject();
  const editable = project.state === 'ACTIVE';
  const [rotation, setRotation] = useState<'normal' | 'emergency' | null>(null);
  const [typed, setTyped] = useState('');
  const title = t(`purpose.${purpose}`);

  async function rotate(emergency: boolean) {
    const res = await api.rotateSigningKey(project.projectId, purpose, emergency);
    onRotated(res.keys);
    notify(emergency ? t('emergencyDone', { purpose: title }) : t('rotated', { purpose: title }));
  }

  return (
    <Card
      title={title}
      description={rotationDays ? t('keysHint', { days: rotationDays }) : undefined}
      actions={
        editable && (
          <>
            <Button variant="outlined" icon="rotate" onPress={() => setRotation('normal')}>
              {t('rotate')}
            </Button>
            <Button variant="danger" icon="warning" onPress={() => setRotation('emergency')}>
              {t('emergency')}
            </Button>
          </>
        )
      }
    >
      {keys.length === 0 ? (
        <p className={styles.muted}>{t('noKeys')}</p>
      ) : (
        <Table caption={title} columns={[t('kid'), t('state'), t('created'), t('activated'), t('retired')]}>
          {keys.map((k) => (
            <Row key={k.kid}>
              <Cell>
                <Ltr>{k.kid}</Ltr>
              </Cell>
              <Cell nowrap>
                <Badge tone={STATE_TONE[k.state]}>{t(`states.${k.state}`)}</Badge>
              </Cell>
              <Cell nowrap>
                <Time value={k.createTime} />
              </Cell>
              <Cell nowrap>{k.activateTime ? <Time value={k.activateTime} /> : '—'}</Cell>
              <Cell nowrap>{k.retireTime ? <Time value={k.retireTime} /> : '—'}</Cell>
            </Row>
          ))}
        </Table>
      )}
      {rotation === 'normal' && (
        <ConfirmDialog title={t('rotateTitle', { purpose: title })} confirmLabel={t('rotate')} onClose={() => setRotation(null)} onConfirm={() => rotate(false)}>
          <p>{t('rotateBody')}</p>
        </ConfirmDialog>
      )}
      {rotation === 'emergency' && (
        <ConfirmDialog
          title={t('emergencyTitle', { purpose: title })}
          confirmLabel={t('emergency')}
          danger
          canConfirm={typed === project.projectId}
          onClose={() => {
            setRotation(null);
            setTyped('');
          }}
          onConfirm={() => rotate(true)}
        >
          <Banner tone="warning">{t('emergencyWarning')}</Banner>
          <p>{t('emergencyBody')}</p>
          <TextField
            label={t('confirmLabel')}
            value={typed}
            onChange={setTyped}
            ltr
            autoFocus
            description={t.rich('confirmHint', { id: () => <Ltr>{project.projectId}</Ltr> })}
          />
        </ConfirmDialog>
      )}
    </Card>
  );
}

function LimitsCard({ limits }: { limits: AuthLimits }) {
  const t = useTranslations('auth.settings');
  const tTypes = useTranslations('auth.templates.types');
  const format = useFormatter();
  const duration = (seconds: number) => {
    const [value, unit] =
      seconds % 86400 === 0 ? [seconds / 86400, 'day'] : seconds % 3600 === 0 ? [seconds / 3600, 'hour'] : seconds % 60 === 0 ? [seconds / 60, 'minute'] : [seconds, 'second'];
    return format.number(value, { style: 'unit', unit, unitDisplay: 'long' });
  };
  const codes = TEMPLATE_TYPES.filter((type) => limits.emailCodeLifetimeSeconds?.[type] !== undefined);

  return (
    <Card title={t('limits')} description={t('limitsHint')}>
      <DetailList>
        <Detail label={t('idTokenLifetime')}>{duration(limits.idTokenLifetimeSeconds)}</Detail>
        <Detail label={t('sessionCookieMax')}>{duration(limits.sessionCookieMaxSeconds)}</Detail>
        <Detail label={t('keyRotation')}>{duration(limits.signingKeyRotationDays * 86400)}</Detail>
        <Detail label={t('requests')}>{t('perMinute', { count: limits.requestsPerMinute, burst: limits.requestBurst })}</Detail>
        <Detail label={t('signUps')}>{t('perMinute', { count: limits.signUpsPerMinute, burst: limits.signUpBurst })}</Detail>
        <Detail label={t('passwordFailures')}>
          {t('failures', {
            count: limits.passwordFailures,
            window: duration(limits.passwordFailureWindowSeconds),
            max: duration(limits.passwordBackoffMaxSeconds),
          })}
        </Detail>
        {codes.map((type) => (
          <Detail key={type} label={t('codeLifetime', { type: tTypes(type) })}>
            {duration(limits.emailCodeLifetimeSeconds?.[type] ?? 0)}
          </Detail>
        ))}
      </DetailList>
    </Card>
  );
}
