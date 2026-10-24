import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { json, renderInProject, stubApi } from '@/test/render';
import type { Listing, StorageInfo, StoredObject } from './api';
import { FilesScreen } from './FilesScreen';
import { ObjectScreen } from './ObjectScreen';
import { RulesScreen } from './RulesScreen';
import { StorageSection } from './StorageSection';

const BASE = '/console/v1/projects/chat-app/storage';

const info: StorageInfo = {
  bucket: 'chat-app.console.example.com',
  enabled: true,
  limits: { objectBytes: 5368709120, projectBytes: 5368709120, metadataBytes: 8192, headerFieldBytes: 1024, listMaxResults: 1000, uploadSessionSeconds: 3600, requestBytes: 1048576 },
};

const object = (name: string, extra: Partial<StoredObject> = {}): StoredObject => ({
  name,
  bucket: info.bucket,
  size: 2048,
  contentType: 'image/png',
  metadata: {},
  downloadTokens: ['t1'],
  downloadUrls: [`http://127.0.0.1/v0/b/${info.bucket}/o/${encodeURIComponent(name)}?alt=media&token=t1`],
  generation: '1784647629568292',
  metageneration: '1',
  md5Hash: 'XrY7u+Ae7tCTyyK7j1rNww==',
  crc32c: 'yZRlqg==',
  timeCreated: '2026-10-24T10:00:00.000Z',
  updated: '2026-10-24T10:00:00.000Z',
  ...extra,
});

type Calls = ReturnType<typeof stubApi>['mock']['calls'];
const callsTo = (calls: Calls, method: string, path: string) =>
  calls
    .filter(([url, init]) => new URL(String(url), 'http://localhost').pathname === path && (init?.method ?? 'GET') === method)
    .map(([url, init]) => ({ query: new URL(String(url), 'http://localhost').searchParams, body: init?.body ? (JSON.parse(String(init.body)) as unknown) : undefined }));

const inSection = (ui: React.ReactElement, search = '') => renderInProject(<StorageSection>{ui}</StorageSection>, { search });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('StorageSection', () => {
  it('shows the bucket and the four tabs', async () => {
    stubApi({ [`GET ${BASE}`]: () => json(200, info) });
    inSection(<p>content</p>);
    expect(await screen.findByText('gs://chat-app.console.example.com')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'الملفات' })).toHaveAttribute('href', '/project/storage/?id=chat-app');
    expect(screen.getByRole('tab', { name: 'القواعد' })).toHaveAttribute('href', '/project/storage/rules/?id=chat-app');
    expect(screen.getByRole('tab', { name: 'الاستعمال' })).toHaveAttribute('href', '/project/storage/usage/?id=chat-app');
    expect(screen.getByRole('tab', { name: 'CORS' })).toHaveAttribute('href', '/project/storage/cors/?id=chat-app');
    expect(screen.getByText('content')).toBeInTheDocument();
  });

  it('says when the server does not serve Storage', async () => {
    stubApi({ [`GET ${BASE}`]: () => json(200, { bucket: info.bucket, enabled: false }) });
    inSection(<p>content</p>);
    expect(await screen.findByText('التخزين السحابي متوقف على هذا الخادم')).toBeInTheDocument();
    expect(screen.queryByText('content')).not.toBeInTheDocument();
  });
});

describe('FilesScreen', () => {
  it('lists a folder’s subfolders and files, and deletes after a confirmation', async () => {
    const listing: Listing = { prefixes: ['photos/2026/'], items: [object('photos/cat.png')] };
    const fetchMock = stubApi({
      [`GET ${BASE}`]: () => json(200, info),
      [`GET ${BASE}/objects`]: () => json(200, listing),
      [`DELETE ${BASE}/object`]: () => json(200, {}),
    });
    inSection(<FilesScreen />, '?id=chat-app&prefix=photos/');
    expect(await screen.findByRole('link', { name: 'فتح المجلد 2026' })).toHaveAttribute('href', '/project/storage/?id=chat-app&prefix=photos%2F2026%2F');
    expect(screen.getByRole('link', { name: 'cat.png' })).toHaveAttribute('href', '/project/storage/object/?id=chat-app&name=photos%2Fcat.png');
    expect(callsTo(fetchMock.mock.calls, 'GET', `${BASE}/objects`)[0]?.query.get('prefix')).toBe('photos/');

    await userEvent.click(screen.getByRole('button', { name: 'حذف photos/cat.png' }));
    const dialog = await screen.findByRole('alertdialog');
    await userEvent.click(within(dialog).getByRole('button', { name: 'حذف' }));
    await waitFor(() => expect(screen.queryByRole('link', { name: 'cat.png' })).not.toBeInTheDocument());
    expect(callsTo(fetchMock.mock.calls, 'DELETE', `${BASE}/object`)[0]?.query.get('name')).toBe('photos/cat.png');
  });
});

describe('ObjectScreen', () => {
  it('saves only the metadata that changed', async () => {
    const o = object('cat.png', { metadata: { owner: 'amal' } });
    const fetchMock = stubApi({
      [`GET ${BASE}`]: () => json(200, info),
      [`GET ${BASE}/object`]: () => json(200, o),
      [`PATCH ${BASE}/object`]: () => json(200, { ...o, cacheControl: 'max-age=60', metadata: {}, metageneration: '2' }),
    });
    inSection(<ObjectScreen />, '?id=chat-app&name=cat.png');
    const cache = await screen.findByLabelText('Cache-Control');
    await userEvent.type(cache, 'max-age=60');
    await userEvent.click(screen.getByRole('button', { name: 'إزالة المفتاح 1' }));
    await userEvent.click(screen.getByRole('button', { name: 'حفظ' }));
    await screen.findByText('حُفظت البيانات الوصفية.');
    expect(callsTo(fetchMock.mock.calls, 'PATCH', `${BASE}/object`)[0]?.body).toEqual({ cacheControl: 'max-age=60', metadata: { owner: null } });
    // An image previews from the console download, in an <img>.
    expect(screen.getByRole('img', { name: 'معاينة cat.png' }).getAttribute('src')).toMatch(/^\/console\/v1\/projects\/chat-app\/storage\/object:download\?name=cat\.png&generation=/);
  });
});

describe('RulesScreen', () => {
  it('starts from the locked rules, and shows a refused publish’s errors on their lines', async () => {
    const issue = { description: 'mismatched input', severity: 'ERROR', sourcePosition: { fileName: 'storage.rules', line: 2, column: 3, currentOffset: 4, endOffset: 7 } };
    const fetchMock = stubApi({
      [`GET ${BASE}`]: () => json(200, info),
      [`GET ${BASE}/rules`]: () => json(200, { published: false, source: '' }),
      [`PUT ${BASE}/rules`]: () =>
        json(400, { error: { code: 400, status: 'INVALID_ARGUMENT', message: 'The rules do not compile.', details: [{ '@type': 'type.aruma.dev/storage.RulesIssues', issues: [issue] }] } }),
    });
    const { container } = inSection(<RulesScreen />);
    const editor = await screen.findByLabelText('القواعد');
    expect((editor as HTMLTextAreaElement).value).toContain('allow read, write: if false;');
    expect(screen.getByText('لم تُنشر قواعد بعد')).toBeInTheDocument();

    await userEvent.clear(editor);
    await userEvent.type(editor, 'x\n  bad');
    await userEvent.click(screen.getByRole('button', { name: 'نشر' }));
    await screen.findByText('في القواعد خطأ');
    expect(callsTo(fetchMock.mock.calls, 'PUT', `${BASE}/rules`)[0]?.body).toEqual({ source: 'x\n  bad' });
    expect(container.querySelector('[data-line="2"]')).toHaveAttribute('data-issue', 'error');
    expect(container.querySelector('[data-line="1"]')).not.toHaveAttribute('data-issue');
    await userEvent.click(screen.getByRole('button', { name: 'السطر 2، العمود 3:' }));
    expect(editor).toHaveFocus();
    expect([(editor as HTMLTextAreaElement).selectionStart, (editor as HTMLTextAreaElement).selectionEnd]).toEqual([4, 7]);
  });
});
