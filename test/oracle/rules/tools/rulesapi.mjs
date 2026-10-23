// Calls the production Rules Test API (firebaserules.googleapis.com v1
// projects.test) with the access token of the firebase CLI login on this
// machine. The token is obtained through firebase-tools' own auth module and
// never leaves this module: it is not logged, printed or written anywhere.
// projects.test evaluates the given source without creating or releasing a
// ruleset, so nothing is deployed and no data is read or written.
//
// FIREBASE_TOOLS points at the firebase-tools package directory
// (default /opt/homebrew/lib/node_modules/firebase-tools).
// RULES_PROJECT is the project the API runs under (default chatapp-8ae21).
import { createRequire } from 'node:module';
import { join } from 'node:path';

const FT = process.env.FIREBASE_TOOLS || '/opt/homebrew/lib/node_modules/firebase-tools';
const require = createRequire(import.meta.url);

export const PROJECT = process.env.RULES_PROJECT || 'chatapp-8ae21';

let cached = null;

async function accessToken() {
  if (cached && cached.expires > Date.now() + 60_000) return cached.token;
  const auth = require(join(FT, 'lib/auth.js'));
  const scopes = require(join(FT, 'lib/scopes.js'));
  const account = auth.getGlobalDefaultAccount();
  if (!account) throw new Error('firebase CLI is not logged in');
  const want = [scopes.EMAIL, scopes.OPENID, scopes.CLOUD_PROJECTS_READONLY, scopes.FIREBASE_PLATFORM, scopes.CLOUD_PLATFORM];
  let t;
  for (let attempt = 0; ; attempt++) {
    try {
      t = await auth.getAccessToken(account.tokens.refresh_token, want);
      break;
    } catch (e) {
      // firebase-tools reports any failed refresh, a network one included,
      // as invalid credentials: try a few times before giving up.
      if (attempt >= 5) throw e;
      await new Promise((r) => setTimeout(r, 10_000 * (attempt + 1)));
    }
  }
  cached = { token: t.access_token, expires: t.expires_at || Date.now() + 30 * 60_000 };
  return cached.token;
}

// test sends one ruleset file and its test cases and returns
// { status, body } with the parsed JSON response.
export async function test(fileName, source, testCases) {
  const body = { source: { files: [{ name: fileName, content: source }] } };
  if (testCases && testCases.length) body.testSuite = { testCases };
  return send(JSON.stringify(body));
}

// send posts a request body that is already JSON text, for the cases where
// JSON.stringify cannot write the value under test (such as 2.0).
export async function send(text0) {
  for (let attempt = 0; ; attempt++) {
    let res, text;
    try {
      res = await fetch(`https://firebaserules.googleapis.com/v1/projects/${PROJECT}:test`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${await accessToken()}`, 'Content-Type': 'application/json' },
        body: text0,
        signal: AbortSignal.timeout(120_000),
      });
      text = await res.text();
    } catch (e) {
      // A network failure or a timeout (no response at all): try again.
      if (attempt < 8) {
        await new Promise((r) => setTimeout(r, 3000 * (attempt + 1)));
        continue;
      }
      throw e;
    }
    if ((res.status === 429 || res.status >= 500) && attempt < 4) {
      await new Promise((r) => setTimeout(r, 2000 * (attempt + 1)));
      continue;
    }
    let parsed;
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = { raw: text };
    }
    return { status: res.status, body: parsed };
  }
}
