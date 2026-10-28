'use client';

import { useState } from 'react';
import { useFormatter, useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import * as api from '../api';
import { LineChart, type Series } from './Chart';
import styles from './UsageScreen.module.css';
import { PERIODS, perDay, points } from './usage';

type Counter = 'reads' | 'writes' | 'deletes' | 'listenersPeak' | 'connectionsPeak' | 'rulesAllows' | 'rulesDenies' | 'rulesErrors';

const COLORS = ['var(--md-sys-color-primary)', 'var(--md-sys-color-tertiary)', 'var(--md-sys-color-error)'];
const DASHES = [undefined, '6 3', '2 3'];

const SECTIONS: { id: 'billable' | 'subscription' | 'rules'; counters: Counter[] }[] = [
  { id: 'billable', counters: ['reads', 'writes', 'deletes'] },
  { id: 'subscription', counters: ['listenersPeak', 'connectionsPeak'] },
  { id: 'rules', counters: ['rulesAllows', 'rulesDenies', 'rulesErrors'] },
];

/**
 * What the database counted for the project over a period, as the Firebase console's Usage
 * tab shows it: the billable reads, writes and deletes, the peaks of listeners and connections,
 * and the rules' decisions, each with its totals and a chart.
 */
export function UsageScreen() {
  const t = useTranslations('documents.usage');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const format = useFormatter();
  const { project } = useProject();
  const [period, setPeriod] = useState<api.UsagePeriod>('24h');
  const usage = useResource((signal) => api.getUsageReport(project.projectId, period, signal), [project.projectId, period]);
  useDocumentTitle(t('title'));

  const report = usage.data?.period === period ? usage.data : undefined;
  const pts = report ? points(report) : [];

  return (
    <Card
      title={t('title')}
      description={t('description')}
      actions={
        <div className={styles.controls}>
          <Select
            label={t('period')}
            value={period}
            onChange={(id) => setPeriod(id as api.UsagePeriod)}
            options={PERIODS.map((p) => ({ id: p, label: t(`periods.${p}`) }))}
          />
          <Button variant="outlined" icon="rotate" isPending={usage.loading} onPress={usage.reload}>
            {t('refresh')}
          </Button>
        </div>
      }
    >
      {usage.error && !report ? (
        <Banner tone="error" action={<Button variant="text" onPress={usage.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(usage.error)}
        </Banner>
      ) : !report ? (
        <Skeleton lines={6} />
      ) : (
        <div className={styles.sections}>
          {SECTIONS.map((section) => {
            const series: Series[] = section.counters.map((c, i) => ({
              id: c,
              label: t(`counter.${c}`),
              color: COLORS[i]!,
              dash: DASHES[i],
              value: (p) => p.row[c],
            }));
            const summary = section.counters.map((c) => `${t(`counter.${c}`)} ${format.number(report.totals[c])}`).join(', ');
            return (
              <section key={section.id} className={styles.section} aria-labelledby={`usage-${section.id}`}>
                <h3 id={`usage-${section.id}`} className={styles.heading}>
                  {t(`section.${section.id}`)}
                </h3>
                <dl className={styles.totals}>
                  {section.counters.map((c) => (
                    <div key={c} className={styles.stat}>
                      <dt>{t(`counter.${c}`)}</dt>
                      <dd data-counter={c}>{format.number(report.totals[c])}</dd>
                    </div>
                  ))}
                </dl>
                <LineChart title={t(`chart.${section.id}.${perDay(period) ? 'day' : 'hour'}`)} summary={summary} points={pts} series={series} perDay={perDay(period)} />
              </section>
            );
          })}
          <p className={styles.note}>{t('note')}</p>
        </div>
      )}
    </Card>
  );
}
