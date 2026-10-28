import { expect, test, type Page } from '@playwright/test';

// The Document Database screens against a real `aruma serve` with this dashboard embedded and
// the Document Database enabled, opened on the server itself (ARUMA_DASHBOARD_URL): browse, add,
// edit over the version read (and lose to a concurrent REST write), delete, recursive delete,
// rules (publish, simulate), an exemption, a TTL policy and the usage counters. The concurrent
// write is a Firestore REST call as the dev owner, so the server runs with
// ARUMA_DOCUMENTS_DEV_OWNER_TOKEN=true (loopback only). See playwright.config.ts.

const email = process.env.ARUMA_E2E_EMAIL ?? 'owner@example.com';
const password = process.env.ARUMA_E2E_PASSWORD ?? 'correct horse battery';

async function signIn(page: Page) {
  await page.goto('/');
  await page.waitForURL(/\/(setup|login|projects)\/$/);
  if (page.url().endsWith('/setup/')) {
    const code = process.env.ARUMA_SETUP_CODE;
    test.skip(!code, 'The server needs first-boot setup: set ARUMA_SETUP_CODE.');
    await page.getByLabel('Setup code').fill(code!);
    await page.getByLabel('Email').fill(email);
    await page.getByLabel('Password').fill(password);
    await page.getByRole('button', { name: 'Create account' }).click();
  } else if (page.url().endsWith('/login/')) {
    await page.getByLabel('Email').fill(email);
    await page.getByLabel('Password').fill(password);
    await page.getByRole('button', { name: 'Sign in' }).click();
  }
  await expect(page).toHaveURL('/projects/');
}

test('Document Database from the first document to the usage counters', async ({ page }) => {
  test.setTimeout(180_000);
  await page.addInitScript(() => localStorage.setItem('aruma.locale', 'en'));
  await signIn(page);
  const projectId = `docs-${Date.now().toString(36)}`;
  await page.getByRole('button', { name: 'New project' }).first().click();
  const created = page.getByRole('dialog');
  await created.getByLabel('Project name').fill('Documents Live');
  await created.getByLabel('Project ID').fill(projectId);
  await expect(created.getByText('Available')).toBeVisible();
  await created.getByRole('button', { name: 'Create project' }).click();
  await expect(page).toHaveURL(`/project/?id=${projectId}`);

  const data = `/project/documents/?id=${projectId}`;
  const fieldsOf = (id: string) => page.getByRole('list', { name: `Fields of ${id}` });
  await page.getByRole('list', { name: 'Build' }).getByRole('link', { name: 'Document Database' }).click();
  await expect(page).toHaveURL(new RegExp(`/project/documents/\\?id=${projectId}`));

  // The first collection and document, a second document, and a third collection to delete.
  await page.getByRole('button', { name: 'Start collection' }).click();
  let dialog = page.getByRole('dialog', { name: 'Start a collection' });
  await dialog.getByLabel('Collection ID').fill('users');
  await dialog.getByRole('button', { name: 'Next' }).click();
  await dialog.getByLabel('Document ID').fill('alice');
  await dialog.getByRole('textbox', { name: 'Field', exact: true }).fill('name');
  await dialog.getByLabel('Value').fill('Alice');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/path=users%2Falice$/);
  await expect(fieldsOf('alice')).toContainText('name: "Alice" (string)');

  await page.goto(`${data}&path=users`);
  await page.getByRole('button', { name: 'Add document' }).click();
  dialog = page.getByRole('dialog', { name: 'Add a document' });
  await dialog.getByLabel('Document ID').fill('bob');
  await dialog.getByRole('textbox', { name: 'Field', exact: true }).fill('name');
  await dialog.getByLabel('Value').fill('Bob');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/path=users%2Fbob$/);

  // A document with a sub-collection, to delete recursively later.
  await page.goto(`${data}&path=scratch`);
  await page.getByRole('button', { name: 'Add document' }).click();
  dialog = page.getByRole('dialog', { name: 'Add a document' });
  await dialog.getByLabel('Document ID').fill('s1');
  await dialog.getByRole('textbox', { name: 'Field', exact: true }).fill('n');
  await dialog.getByLabel('Value').fill('1');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/path=scratch%2Fs1$/);
  await page.goto(`${data}&path=scratch%2Fs1%2Fnested`);
  await page.getByRole('button', { name: 'Add document' }).click();
  dialog = page.getByRole('dialog', { name: 'Add a document' });
  await dialog.getByLabel('Document ID').fill('n1');
  await dialog.getByRole('textbox', { name: 'Field', exact: true }).fill('n');
  await dialog.getByLabel('Value').fill('2');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/path=scratch%2Fs1%2Fnested%2Fn1$/);

  // Browse: the root lists the collections, the collection its documents.
  await page.goto(data);
  const roots = page.getByRole('list', { name: 'Collections of /' });
  await expect(roots.getByRole('link', { name: 'users' })).toBeVisible();
  await expect(roots.getByRole('link', { name: 'scratch' })).toBeVisible();
  await page.goto(`${data}&path=users`);
  await expect(page.getByRole('link', { name: 'alice' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'bob' })).toBeVisible();

  // An edit carries the updateTime it read. A concurrent REST write makes it fail; after a
  // reload the same edit goes through.
  await page.goto(`${data}&path=users%2Falice`);
  const patches: { updateTime?: string; updateMask?: string[] }[] = [];
  page.on('request', (r) => {
    if (r.method() === 'PATCH' && r.url().includes('/documents/document')) patches.push(r.postDataJSON() as { updateTime?: string; updateMask?: string[] });
  });
  await page.getByRole('button', { name: 'Edit field name' }).click();
  dialog = page.getByRole('dialog', { name: 'Edit field' });
  const rest = await page.request.patch(
    `/v1/projects/${projectId}/databases/(default)/documents/users/alice?updateMask.fieldPaths=other`,
    { headers: { Authorization: 'Bearer owner' }, data: { fields: { other: { stringValue: 'from REST' } } } },
  );
  expect(rest.status()).toBe(200);
  await dialog.getByLabel('Value').fill('Alice B');
  await dialog.getByRole('button', { name: 'Update' }).click();
  await expect(dialog.getByText('This document changed since you opened it.', { exact: false })).toBeVisible();
  await dialog.getByRole('button', { name: 'Reload document' }).click();
  await expect(fieldsOf('alice')).toContainText('other: "from REST" (string)');
  await page.getByRole('button', { name: 'Edit field name' }).click();
  await dialog.getByLabel('Value').fill('Alice B');
  await dialog.getByRole('button', { name: 'Update' }).click();
  await expect(fieldsOf('alice')).toContainText('name: "Alice B" (string)');
  expect(patches).toHaveLength(2);
  expect(patches.every((p) => /Z$/.test(p.updateTime ?? ''))).toBe(true);
  expect(patches[0]!.updateTime).not.toBe(patches[1]!.updateTime);

  // A field is deleted; the others stay.
  await page.getByRole('button', { name: 'Delete field other' }).click();
  await page.getByRole('alertdialog', { name: 'Delete Data' }).getByRole('button', { name: 'Delete' }).click();
  await expect(fieldsOf('alice')).not.toContainText('other:');
  await expect(fieldsOf('alice')).toContainText('name: "Alice B" (string)');

  // A collection and everything under it goes.
  await page.goto(`${data}&path=scratch`);
  await page.getByRole('button', { name: 'More actions for collection scratch' }).click();
  await page.getByRole('menuitem', { name: 'Delete collection' }).click();
  const del = page.getByRole('alertdialog', { name: 'Delete this collection?' });
  await del.getByLabel('Collection ID').fill('scratch');
  await del.getByRole('button', { name: 'Delete' }).click();
  await expect(del).toHaveCount(0);
  await page.goto(data);
  await expect(page.getByRole('list', { name: 'Collections of /' }).getByRole('link', { name: 'users' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'scratch' })).toHaveCount(0);

  // The rules: locked at first, then published, then judged by the Playground.
  await page.goto(`/project/documents/rules/?id=${projectId}`);
  const editor = page.getByRole('textbox', { name: 'Rules', exact: true });
  await editor.fill(
    "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n    match /users/{id} {\n      allow read: if request.auth != null;\n    }\n  }\n}\n",
  );
  await page.getByRole('button', { name: 'Publish', exact: true }).click();
  await expect(page.getByText('Rules published.')).toBeVisible();
  await page.reload();
  await expect(editor).toHaveValue(/request\.auth != null/);
  await page.getByRole('button', { name: /Rules Playground/ }).click();
  await page.getByRole('textbox', { name: 'Location' }).fill('users/alice');
  await page.getByRole('button', { name: 'Run', exact: true }).click();
  const result = page.getByTestId('simulation');
  await expect(result).toContainText('Simulated read denied');
  await page.getByText('Authenticated', { exact: true }).click();
  await page.getByRole('textbox', { name: 'Firebase UID' }).fill('alice');
  await page.getByRole('button', { name: 'Run', exact: true }).click();
  await expect(result).toContainText('Simulated read allowed');

  // An exemption, then a TTL policy.
  await page.goto(`/project/documents/indexes/?id=${projectId}`);
  await page.getByRole('tab', { name: 'Automatic' }).click();
  await page.getByRole('button', { name: 'Add exemption' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Collection ID' }).fill('users');
  await dialog.getByRole('textbox', { name: 'Field path' }).fill('bio');
  await dialog.getByRole('button', { name: 'Next' }).click();
  await dialog.getByText('Arrays', { exact: true }).click();
  await dialog.getByRole('button', { name: 'Save' }).click();
  const row = page.getByRole('row', { name: /bio/ });
  await expect(row).toContainText('users');
  await expect(row).toContainText('Arrays (disabled)');

  await page.getByRole('tab', { name: 'Time to live' }).click();
  await page.getByRole('button', { name: 'Create policy' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Collection group' }).fill('sessions');
  await dialog.getByRole('textbox', { name: 'Timestamp field' }).fill('expiresAt');
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();
  const policy = page.getByRole('row', { name: /expiresAt/ });
  await expect(policy).toContainText('sessions');
  // A second policy in the group is refused by the console before any request.
  await page.getByRole('button', { name: 'Create policy' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByRole('textbox', { name: 'Collection group' }).fill('sessions');
  await dialog.getByRole('textbox', { name: 'Timestamp field' }).fill('other');
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(dialog.getByText('You can set one TTL policy per collection group.')).toHaveCount(2);
  await dialog.getByRole('button', { name: 'Cancel' }).click();

  // The browsing and writing above are counted.
  await page.goto(`/project/documents/usage/?id=${projectId}`);
  const reads = page.locator('[data-counter="reads"]');
  await expect.poll(async () => Number(await reads.getAttribute('data-value')), { timeout: 30_000 }).toBeGreaterThan(0);
  await expect.poll(async () => Number(await page.locator('[data-counter="writes"]').getAttribute('data-value'))).toBeGreaterThan(0);

  // The project goes.
  await page.goto(`/project/settings/?id=${projectId}`);
  await page.getByRole('button', { name: 'Delete project' }).click();
  await page.getByRole('alertdialog').getByLabel('Project ID').fill(projectId);
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete project' }).click();
  await expect(page.getByText('This project is deleted')).toBeVisible();
});
