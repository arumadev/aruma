import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { DatabaseInfo, DataPage } from '@/lib/api/types';
import { json, renderInProject, stubApi } from '@/test/render';
import { DatabaseSection } from './DatabaseSection';
import { DataScreen } from './DataScreen';
import { RulesScreen } from './RulesScreen';
import { DEFAULT_LIMITS } from './validation';

const BASE = '/console/v1/projects/chat-app/database';

const info: DatabaseInfo = {
  name: 'chat-app-default-rtdb',
  databaseURL: 'https://chat-app-default-rtdb.rtdb.example.com',
  enabled: true,
  devOpenRules: false,
  writeCount: 3,
  size: { rows: 3, bytes: 20, capped: false },
  limits: DEFAULT_LIMITS,
};

const page = (children: DataPage['children'], nextPageToken = '', value?: DataPage['value']): DataPage => ({
  path: '/',
  exists: children.length > 0 || value !== undefined,
  children,
  nextPageToken,
  ...(value !== undefined && { value }),
});

type Calls = ReturnType<typeof stubApi>['mock']['calls'];
const callsTo = (calls: Calls, method: string, path: string) =>
  calls
    .filter(([url, init]) => new URL(String(url), 'http://localhost').pathname === path && (init?.method ?? 'GET') === method)
    .map(([url, init]) => ({ query: new URL(String(url), 'http://localhost').searchParams, body: init?.body ? (JSON.parse(String(init.body)) as unknown) : undefined }));

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('DatabaseSection', () => {
  it('shows the database URL to copy and the two tabs', async () => {
    stubApi({ [`GET ${BASE}`]: () => json(200, info) });
    renderInProject(<DatabaseSection>content</DatabaseSection>);
    expect(await screen.findByText(info.databaseURL)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'نسخ رابط قاعدة البيانات' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'البيانات' })).toHaveAttribute('href', '/project/database/?id=chat-app');
    expect(screen.getByRole('tab', { name: 'القواعد' })).toHaveAttribute('href', '/project/database/rules/?id=chat-app');
    expect(screen.getByText('content')).toBeInTheDocument();
  });

  it('says when the server does not serve the database', async () => {
    stubApi({ [`GET ${BASE}`]: () => json(200, { name: info.name, databaseURL: 'http://127.0.0.1:8080/?ns=chat-app-default-rtdb', enabled: false }) });
    renderInProject(<DatabaseSection>content</DatabaseSection>);
    expect(await screen.findByText('قاعدة البيانات اللحظية متوقفة على هذا الخادم')).toBeInTheDocument();
    expect(screen.queryByText('content')).not.toBeInTheDocument();
    expect(screen.getByText('http://127.0.0.1:8080/?ns=chat-app-default-rtdb')).toBeInTheDocument();
  });

  it('warns about development rules', async () => {
    stubApi({ [`GET ${BASE}`]: () => json(200, { ...info, devOpenRules: true }) });
    renderInProject(<DatabaseSection>content</DatabaseSection>);
    expect(await screen.findByText('قواعد التطوير مفعّلة')).toBeInTheDocument();
  });
});

describe('DataScreen', () => {
  const tree: Record<string, DataPage> = {
    '/': page([{ key: 'count', value: 5 }, { key: 'users', hasChildren: true }]),
    '/users': page([{ key: 'amal', value: 'أمل' }, { key: 'bio', value: 'x'.repeat(1000), truncated: true }]),
    '/users/bio': page([], '', 'x'.repeat(1500)),
  };
  const routes = () =>
    stubApi({
      [`GET ${BASE}`]: () => json(200, info),
      [`GET ${BASE}/data`]: (_, url) => json(200, tree[url.searchParams.get('path') ?? '/'] ?? page([])),
      [`PUT ${BASE}/data`]: () => new Response(null, { status: 204 }),
      [`DELETE ${BASE}/data`]: () => new Response(null, { status: 204 }),
    });
  const renderData = () =>
    renderInProject(
      <DatabaseSection>
        <DataScreen />
      </DatabaseSection>,
    );

  it('opens nodes on demand and edits a leaf in place', async () => {
    const fetchMock = routes();
    renderData();
    expect(await screen.findByText('count')).toBeInTheDocument();
    expect(screen.getByText('5')).toBeInTheDocument();
    expect(callsTo(fetchMock.mock.calls, 'GET', `${BASE}/data`)).toHaveLength(1);

    await userEvent.click(screen.getByRole('button', { name: 'توسيع users' }));
    expect(await screen.findByText('"أمل"')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'طي users' })).toHaveAttribute('aria-expanded', 'true');

    await userEvent.click(screen.getByRole('button', { name: 'تعديل count' }));
    const value = screen.getByLabelText('القيمة');
    expect(value).toHaveValue('5');
    await userEvent.clear(value);
    await userEvent.type(value, '6');
    expect(screen.getByText('يُحفظ رقمًا.')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'حفظ' }));
    await waitFor(() => expect(callsTo(fetchMock.mock.calls, 'PUT', `${BASE}/data`)).toHaveLength(1));
    const [put] = callsTo(fetchMock.mock.calls, 'PUT', `${BASE}/data`);
    expect(put?.query.get('path')).toBe('/count');
    expect(put?.body).toEqual({ value: 6 });
  });

  it('edits a long string whole', async () => {
    const fetchMock = routes();
    renderData();
    await userEvent.click(await screen.findByRole('button', { name: 'توسيع users' }));
    await userEvent.click(await screen.findByRole('button', { name: 'تعديل bio' }));
    expect(await screen.findByLabelText('القيمة')).toHaveValue('x'.repeat(1500));
    expect(callsTo(fetchMock.mock.calls, 'GET', `${BASE}/data`).some((c) => c.query.get('path') === '/users/bio')).toBe(true);
  });

  it('checks a new key before sending it', async () => {
    const fetchMock = routes();
    renderData();
    await userEvent.click(await screen.findByRole('button', { name: 'إضافة ابن' }));
    await userEvent.type(screen.getByLabelText('المفتاح'), 'a.b');
    await userEvent.type(screen.getByLabelText('القيمة'), 'hello');
    await userEvent.click(screen.getByRole('button', { name: 'إضافة' }));
    expect(screen.getByText('لا يحتوي المفتاح على . # $ [ ] / ولا على محارف التحكم.')).toBeInTheDocument();
    expect(callsTo(fetchMock.mock.calls, 'PUT', `${BASE}/data`)).toHaveLength(0);

    await userEvent.clear(screen.getByLabelText('المفتاح'));
    await userEvent.type(screen.getByLabelText('المفتاح'), 'greeting');
    await userEvent.clear(screen.getByLabelText('القيمة'));
    await userEvent.type(screen.getByLabelText('القيمة'), '{{"a": ');
    expect(screen.getByText('هذا ليس JSON صحيحًا. راجع الأقواس وعلامات التنصيص.')).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('القيمة'), '1}');
    await userEvent.click(screen.getByRole('button', { name: 'إضافة' }));
    await waitFor(() => expect(callsTo(fetchMock.mock.calls, 'PUT', `${BASE}/data`)).toHaveLength(1));
    const [put] = callsTo(fetchMock.mock.calls, 'PUT', `${BASE}/data`);
    expect(put?.query.get('path')).toBe('/greeting');
    expect(put?.body).toEqual({ value: { a: 1 } });
  });

  it('asks in the page before deleting', async () => {
    const fetchMock = routes();
    renderData();
    await userEvent.click(await screen.findByRole('button', { name: 'حذف users' }));
    const dialog = screen.getByRole('alertdialog');
    expect(within(dialog).getByText('/users')).toBeInTheDocument();
    expect(callsTo(fetchMock.mock.calls, 'DELETE', `${BASE}/data`)).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole('button', { name: 'حذف' }));
    await waitFor(() => expect(callsTo(fetchMock.mock.calls, 'DELETE', `${BASE}/data`)).toHaveLength(1));
    expect(callsTo(fetchMock.mock.calls, 'DELETE', `${BASE}/data`)[0]?.query.get('path')).toBe('/users');
  });

  it('loads a big list a page at a time', async () => {
    const first = Array.from({ length: 50 }, (_, i) => ({ key: `k${String(i).padStart(3, '0')}`, value: i }));
    const fetchMock = stubApi({
      [`GET ${BASE}`]: () => json(200, info),
      [`GET ${BASE}/data`]: (_, url) =>
        json(200, url.searchParams.get('pageToken') ? page([{ key: 'k050', value: 50 }]) : page(first, 'azA1MA')),
    });
    renderData();
    expect(await screen.findByText('k049')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'عرض المزيد' }));
    expect(await screen.findByText('k050')).toBeInTheDocument();
    expect(screen.getByText('k000')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'عرض المزيد' })).not.toBeInTheDocument();
    expect(callsTo(fetchMock.mock.calls, 'GET', `${BASE}/data`)[1]?.query.get('pageToken')).toBe('azA1MA');
  });
});

describe('RulesScreen', () => {
  const locked = '{"rules": {".read": false, ".write": false}}';

  it('shows a refused save at its line, then publishes the fix', async () => {
    let saved = locked;
    const fetchMock = stubApi({
      [`GET ${BASE}/rules`]: () => json(200, { rules: saved }),
      [`PUT ${BASE}/rules`]: (init) => {
        const { rules } = JSON.parse(String(init.body)) as { rules: string };
        if (rules.includes('auth.uid ==')) {
          return json(400, {
            error: {
              code: 400,
              status: 'INVALID_ARGUMENT',
              message: 'The rules are not valid',
              details: [
                { '@type': 'type.googleapis.com/google.rpc.BadRequest', fieldViolations: [{ field: 'rules', description: '3:15: Unexpected end' }] },
                { '@type': 'type.aruma.dev/rtdb.RulesErrors', errors: [{ line: 3, column: 15, path: 'rules/.write', message: 'Unexpected end of expression.' }] },
              ],
            },
          });
        }
        saved = rules;
        return json(200, { rules });
      },
    });
    renderInProject(<RulesScreen />);
    const editor = await screen.findByLabelText('القواعد (JSON)');
    expect(editor).toHaveValue(locked);
    expect(screen.getByRole('button', { name: 'نشر' })).toBeDisabled();

    await userEvent.clear(editor);
    await userEvent.type(editor, '{{\n"rules": {{\n".write": "auth.uid =="\n}\n}');
    await userEvent.click(screen.getByRole('button', { name: 'نشر' }));
    expect(await screen.findByText('في القواعد خطأ')).toBeInTheDocument();
    expect(screen.getByText('Unexpected end of expression. (rules/.write)')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'السطر 3، العمود 15:' }));
    expect(editor).toHaveFocus();

    await userEvent.clear(editor);
    await userEvent.type(editor, '{{"rules": {{".write": "auth != null"}}');
    expect(screen.queryByText('في القواعد خطأ')).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'نشر' }));
    await waitFor(() => expect(saved).toBe('{"rules": {".write": "auth != null"}}'));
    expect(callsTo(fetchMock.mock.calls, 'PUT', `${BASE}/rules`)).toHaveLength(2);
  });

  it('warns while the rules open the database to everyone', async () => {
    stubApi({ [`GET ${BASE}/rules`]: () => json(200, { rules: locked }) });
    renderInProject(<RulesScreen />);
    await screen.findByLabelText('القواعد (JSON)');
    expect(screen.queryByText('بياناتك متاحة للجميع')).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'وضع الاختبار' }));
    expect(screen.getByText('بياناتك متاحة للجميع')).toBeInTheDocument();
    expect(screen.getByLabelText('القواعد (JSON)')).toHaveValue('{\n  "rules": {\n    ".read": true,\n    ".write": true\n  }\n}\n');
    await userEvent.click(screen.getByRole('button', { name: 'الوضع المقفل' }));
    expect(screen.queryByText('بياناتك متاحة للجميع')).not.toBeInTheDocument();
  });
});
