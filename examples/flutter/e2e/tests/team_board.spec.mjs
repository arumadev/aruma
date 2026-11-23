// The Team Board scenario on the web build, against a real Aruma with the rules of rules/:
// two users (two browser contexts) and a guest, through every screen and service.
import { arumaURL, expect, test } from './fixtures.mjs';
import { chooseFile, fill, gone, node, online, openApp, see, tap, tops } from './app.mjs';

test('two users, a guest, one team: Auth, Firestore, Realtime Database, Storage, rules', async ({ browser, network }) => {
  const run = Date.now().toString(36);
  const ada = { name: `Ada ${run}`, email: `ada-${run}@example.com`, password: 'ada-secret-1' };
  const ben = { name: `Ben ${run}`, email: `ben-${run}@example.com`, password: 'ben-secret-1' };
  const teamName = `Rockets ${run}`;

  // --- Sign-up: a weak password is refused, then the account is made.
  const a = await openApp(browser, network);
  await tap(a.page, 'No account? Create one');
  await fill(a.page, 'Name', ada.name);
  await fill(a.page, 'Email', ada.email);
  await fill(a.page, 'Password', '123');
  await tap(a.page, 'Create account');
  await see(a.page, 'This password is too weak. Use at least 6 characters.');
  await fill(a.page, 'Password', ada.password);
  await tap(a.page, 'Create account');
  await see(a.page, 'My teams');
  await see(a.page, 'No teams yet');

  // --- Create a team: the team document and the owner's member document in one batch.
  await tap(a.page, 'Create team');
  await fill(a.page, 'Team name', teamName);
  await tap(a.page, 'Create');
  await see(a.page, /Team created\. Its join code is [A-Z0-9]{6}\./);
  const code = /join code is ([A-Z0-9]{6})/.exec(await node(a.page, /Team created/).textContent())[1];
  await see(a.page, 'Tasks');

  // --- The second user: a wrong password first, then sign-up, a wrong code, the right one.
  const b = await openApp(browser, network);
  await fill(b.page, 'Email', ada.email);
  await fill(b.page, 'Password', 'not-her-password');
  await tap(b.page, 'Sign in');
  await see(b.page, 'Wrong email or password.');
  await tap(b.page, 'No account? Create one');
  await fill(b.page, 'Name', ben.name);
  await fill(b.page, 'Email', ben.email);
  await fill(b.page, 'Password', ben.password);
  await tap(b.page, 'Create account');
  await see(b.page, 'No teams yet');
  await tap(b.page, 'Join team');
  await fill(b.page, 'Join code', 'NOPE42');
  await tap(b.page, 'Join');
  await see(b.page, 'No team has this code.');
  await fill(b.page, 'Join code', code);
  await tap(b.page, 'Join');
  await see(b.page, `You joined ${teamName}.`);
  await see(b.page, 'No tasks yet');

  // --- Presence: Ada sees Ben come online.
  await tap(a.page, 'Chat');
  await expect.poll(online(a.page)).toMatch(new RegExp(`^Online: .*${ben.name}`));

  // --- Tasks, live both ways; ticked off by the other user; open ones first.
  await tap(a.page, 'Tasks');
  await fill(a.page, 'New task', 'Write the brief');
  await tap(a.page, 'Add task');
  await see(b.page, /Write the brief/);
  await fill(b.page, 'New task', 'Book the room');
  await tap(b.page, 'Add task');
  await see(a.page, /Book the room/);
  await b.page.locator('flt-semantics-host').getByRole('checkbox', { name: 'Done: Write the brief' }).click();
  await see(a.page, 'Done (1)');
  await see(a.page, 'Open (1)');
  const [openTop, doneTop] = await tops(a.page, [/Book the room/, /Write the brief/]);
  expect(openTop, 'the open task is above the done one').toBeLessThan(doneTop);

  // --- A rule refuses: Ben may not delete Ada's task (firestore.rules), and says so.
  await tap(b.page, 'More for Write the brief');
  await tap(b.page, 'Delete');
  await see(b.page, "You don't have permission to do that.");
  await see(a.page, /Write the brief/);

  // --- An attachment: Ada uploads (members only, storage.rules), Ben downloads it from the link.
  const content = `the brief, run ${run}\n`;
  await tap(a.page, 'More for Write the brief');
  await chooseFile(a.page, 'Attach a file', { name: 'brief.txt', mimeType: 'text/plain', buffer: Buffer.from(content) });
  await see(a.page, 'File attached');
  await see(b.page, 'brief.txt');
  const popup = b.context.waitForEvent('page');
  // An ActionChip: Flutter's semantics make it a checkbox named by its tooltip and its label.
  await b.page.locator('flt-semantics-host').getByRole('checkbox', { name: /^Open brief\.txt/ }).click();
  const opened = await popup;
  await opened.waitForLoadState();
  const link = opened.url();
  expect(link.startsWith(`${arumaURL}/v0/b/`), `the download link is on Aruma: ${link}`).toBe(true);
  expect(link).toContain('alt=media&token=');
  expect(await (await b.context.request.get(link)).text()).toBe(content);
  await opened.close();

  // --- Chat: three messages from two users, in order on both screens.
  await tap(a.page, 'Chat');
  await tap(b.page, 'Chat');
  for (const [p, text] of [[a.page, 'first'], [b.page, 'second'], [a.page, 'third']]) {
    await fill(p, 'Message', text);
    await tap(p, 'Send');
    await see(a.page, text);
    await see(b.page, text);
  }
  for (const p of [a.page, b.page]) {
    const [y1, y2, y3] = await tops(p, ['first', 'second', 'third']);
    expect(y1).toBeLessThan(y2);
    expect(y2).toBeLessThan(y3);
  }

  // --- Presence goes: Ben leaves the team screen (the app removes it), comes back, then closes
  // the browser (the server's onDisconnect removes it).
  await tap(b.page, 'Back');
  await see(b.page, 'My teams');
  await expect.poll(online(a.page)).not.toMatch(new RegExp(ben.name));
  await tap(b.page, new RegExp(teamName));
  await expect.poll(online(a.page)).toMatch(new RegExp(`^Online: .*${ben.name}`));
  await b.context.close();
  await expect.poll(online(a.page), { timeout: 60_000 }).not.toMatch(new RegExp(ben.name));

  // --- A guest joins by code and writes in the chat.
  const g = await openApp(browser, network);
  await tap(g.page, 'Continue as guest');
  await see(g.page, 'No teams yet');
  await tap(g.page, 'Join team');
  await fill(g.page, 'Join code', code);
  await tap(g.page, 'Join');
  await tap(g.page, 'Chat');
  await see(g.page, 'third');
  await fill(g.page, 'Message', 'hello from a guest');
  await tap(g.page, 'Send');
  await see(a.page, 'hello from a guest');
  await see(a.page, /Guest \d{4}/);
  await g.context.close();

  // --- Profile: a picture (avatars/, image under 2 MB), removed again through
  // refFromURL(fromArumaUrl(link)); then Arabic, right to left.
  await tap(a.page, 'Back');
  await tap(a.page, 'Profile');
  await see(a.page, `Signed in as ${ada.email}`);
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64');
  await chooseFile(a.page, 'Change picture', { name: 'me.png', mimeType: 'image/png', buffer: png });
  await see(a.page, 'Remove picture');
  await tap(a.page, 'Remove picture');
  await gone(a.page, 'Remove picture');

  await tap(a.page, 'Arabic');
  await see(a.page, 'الملف الشخصي');
  const back = await a.page.locator('flt-semantics-host').getByRole('button', { name: 'رجوع' }).boundingBox();
  expect(back.x, 'the back button is on the right in Arabic').toBeGreaterThan(412 / 2);
  await tap(a.page, 'رجوع');
  await see(a.page, 'فرقي');
  await see(a.page, new RegExp(teamName));

  // --- A reload restores the session (and the language): straight to the teams.
  await a.page.reload();
  await a.page.locator('flt-semantics-host').waitFor({ state: 'attached', timeout: 60_000 });
  await see(a.page, 'فرقي');
  await see(a.page, new RegExp(teamName));
  await gone(a.page, 'تسجيل الدخول');

  expect(a.page.errors, 'uncaught errors in the page').toEqual([]);
  await a.context.close();
  // Auth and Storage were reached on Aruma (the Realtime Database's WebSocket and Firestore's
  // WebChannel go there too, or nothing above would have worked; route() does not see a WebSocket).
  for (const prefix of ['POST /identitytoolkit.googleapis.com/', 'POST /v0/b/', 'GET /v0/b/']) {
    expect(network.aruma.some((r) => r.startsWith(prefix)), `a request ${prefix}…`).toBe(true);
  }
});
