import '../netguard.mjs';
import { attempts } from '../netguard.mjs';
import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const dir = process.env.ARUMA_SMOKE_DIR ?? new URL('../.generated/', import.meta.url).pathname;
const config = JSON.parse(readFileSync(`${dir}/firebase-config.json`, 'utf8'));
const expected = JSON.parse(readFileSync(`${dir}/expected.json`, 'utf8'));

for (const major of ['11', '12', '13']) {
  test(`firebase@${major} initializeApp accepts the Aruma web config`, async () => {
    const { initializeApp, deleteApp, SDK_VERSION } = await import(`firebase-${major}/app`);
    const { getInstallations } = await import(`firebase-${major}/installations`);
    assert.ok(SDK_VERSION.startsWith(`${major}.`), `loaded ${SDK_VERSION}`);

    const app = initializeApp(config, `aruma-${major}`);
    assert.equal(app.options.apiKey, expected.web.apiKey);
    assert.equal(app.options.appId, expected.web.appId);
    assert.equal(app.options.projectId, expected.projectId);
    assert.equal(app.options.messagingSenderId, expected.projectNumber);
    assert.equal(app.options.storageBucket, expected.storageBucket);
    assert.equal(app.options.authDomain, expected.authDomain);
    assert.equal(app.options.databaseURL, expected.databaseURL);

    // Installations validates the config it needs (apiKey, appId, projectId) without a request.
    getInstallations(app);
    await deleteApp(app);
  });
}

after(() => assert.deepEqual(attempts, [], `network access attempted: ${attempts.join(', ')}`));
