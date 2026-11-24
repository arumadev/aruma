import { test, expect, value, call } from './fixtures.mjs';

// While Aruma cannot reach Postgres, its Auth endpoints must answer so that the official SDK
// takes the failure for a temporary network error: the SDK then keeps the signed-in user it
// persisted, and retries. Any other error makes it drop that user when the app starts
// (reloadAndSetCurrentUserOrClear: "Something's wrong with the user's token. Log them out") and
// stop refreshing the ID token. The server's database link goes down through genconfig's
// -db-switch, as a stopped Postgres does.

const dbSwitch = process.env.ARUMA_DB_SWITCH_URL;

async function database(state) {
  const res = await fetch(`${dbSwitch}/${state}`, { method: 'POST' });
  expect(res.ok, `database switch ${state}`).toBe(true);
}

test.afterEach(async () => {
  if (dbSwitch) await database('up');
});

test('a signed-in user survives an app start and a token refresh during a database outage', async ({ app, firebaseMajor }) => {
  test.skip(!dbSwitch, 'ARUMA_DB_SWITCH_URL is not set: run through test/e2e/web-auth/run.sh');
  const addr = `outage-${firebaseMajor}@example.com`;
  const uid = await value(app, (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, a.addr, 'secret1').then((c) => c.user.uid), { addr });

  await database('down');
  // The app starts again while Postgres is away: the SDK reloads the persisted user
  // (accounts:lookup), which fails.
  await app.reload();
  await expect(app.locator('#status')).toHaveText(new RegExp(`^ready ${firebaseMajor}\\.`));
  const started = await value(app, async (auth) => {
    await auth.authStateReady();
    return auth.currentUser?.uid ?? null;
  });
  expect(started, 'the user after an app start during the outage').toBe(uid);

  // A forced refresh fails as a network error, which the SDK retries, and keeps the user.
  expect(await call(app, (auth) => auth.currentUser.getIdToken(true))).toEqual({ code: 'auth/network-request-failed' });
  // A sign-in fails the same way.
  expect(await call(app, (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.addr, 'secret1'), { addr })).toEqual({ code: 'auth/network-request-failed' });
  expect(await value(app, (auth) => auth.currentUser?.uid ?? null)).toBe(uid);

  await database('up');
  // Back: the token refreshes, the user is the same, and was never signed out.
  const refreshed = await value(app, async (auth) => {
    const deadline = Date.now() + 15000;
    for (;;) {
      try {
        const token = await auth.currentUser.getIdToken(true);
        return { uid: auth.currentUser.uid, token: token.length > 0 };
      } catch (e) {
        if (Date.now() > deadline) throw e;
        await new Promise((r) => setTimeout(r, 250));
      }
    }
  });
  expect(refreshed).toEqual({ uid, token: true });
  expect(await value(app, () => window.e2e.authStates)).not.toContain(null);
});
