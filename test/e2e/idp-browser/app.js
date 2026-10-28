// The app under test: the official web SDK with the one line Aruma asks for (ADR-013).
import { initializeApp } from 'firebase/app';
import {
  getAuth, connectAuthEmulator, GoogleAuthProvider, OAuthProvider, signInWithPopup, signInWithRedirect,
  getRedirectResult, linkWithPopup, reauthenticateWithPopup, signInAnonymously, signOut,
} from 'firebase/auth';

const cfg = window.__CONFIG;
const app = initializeApp({ apiKey: cfg.apiKey, authDomain: cfg.authDomain, projectId: cfg.projectId });
const auth = getAuth(app);
connectAuthEmulator(auth, cfg.arumaURL, { disableWarnings: true });

const google = (hint) => {
  const p = new GoogleAuthProvider();
  p.setCustomParameters({ login_hint: `${hint}-${cfg.run}` });
  return p;
};
const apple = () => {
  const p = new OAuthProvider('apple.com');
  p.addScope('email');
  p.addScope('name');
  return p;
};

function describe(cred) {
  const u = cred.user;
  return {
    uid: u.uid, email: u.email, emailVerified: u.emailVerified, displayName: u.displayName, isAnonymous: u.isAnonymous,
    providers: u.providerData.map((p) => p.providerId), operationType: cred.operationType,
    oauthIdToken: !!(GoogleAuthProvider.credentialFromResult(cred) || OAuthProvider.credentialFromResult(cred) || {}).idToken,
  };
}

async function run(name, fn) {
  try {
    window.__results[name] = { ok: describe(await fn()) };
  } catch (e) {
    window.__results[name] = { error: e.code || String(e) };
  }
}

window.__results = {};
window.__steps = {
  googlePopup: () => run('googlePopup', () => signInWithPopup(auth, google('popup'))),
  applePopup: () => run('applePopup', () => signInWithPopup(auth, apple())),
  googleRedirect: () => signInWithRedirect(auth, google('redirect')),
  anonymousThenLink: () => run('anonymousThenLink', async () => {
    const anon = await signInAnonymously(auth);
    window.__results.anonymousUid = anon.user.uid;
    return linkWithPopup(anon.user, google('link'));
  }),
  reauth: () => run('reauth', () => reauthenticateWithPopup(auth.currentUser, google('link'))),
  signOut: () => signOut(auth),
};
run('redirectResult', async () => {
  const r = await getRedirectResult(auth);
  if (!r) throw Object.assign(new Error('none'), { code: 'none' });
  return r;
}).then(() => { window.__ready = true; });
