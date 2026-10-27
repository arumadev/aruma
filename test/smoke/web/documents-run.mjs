// Runs documents.test.mjs (Firestore lite of firebase 11, 12 and 13) against a live Aruma with
// the Document Database (npm run test:documents): genconfig -documents-open-rules, so a web
// client without credentials is admitted from this machine (documents-server.mjs).
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { buildGenconfig, startServer, runGuarded } from '../documents-server.mjs';

const here = fileURLToPath(new URL('.', import.meta.url));
const gen = buildGenconfig();
let code = 1;
try {
  const server = await startServer(gen.bin, ['-documents-open-rules']);
  try {
    // documents.test.mjs is Firestore lite (REST); listen.test.mjs the full SDK (gRPC in Node).
    code = 0;
    for (const file of ['documents.test.mjs', 'listen.test.mjs']) {
      code ||= await runGuarded(join(here, file), here, {
        ARUMA_SMOKE_DIR: server.dir,
        ARUMA_SMOKE_DOCUMENTS_ADDR: server.addr,
        ARUMA_NETGUARD_LOG: join(server.dir, 'netguard.log'),
      });
    }
  } finally {
    await server.stop();
  }
} finally {
  gen.remove();
}
process.exit(code);
