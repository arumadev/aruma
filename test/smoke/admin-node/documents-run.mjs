// Runs documents.test.mjs against a live Aruma with the Document Database (npm run
// test:documents), once per transport, each on a server of its own (documents-server.mjs):
//
//   grpc  genconfig -documents-dev-owner: firebase-admin sends Bearer owner on gRPC with ssl:false
//         (ADR-029 §6);
//   rest  genconfig -documents: preferRest sends the key's JWT, and nothing else admits it.
//
// Environment:
//   ARUMA_SMOKE_DOCUMENTS_TRANSPORTS  the transports to run, comma-separated (default grpc,rest)
//   and those of documents-server.mjs.
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { buildGenconfig, startServer, runGuarded } from '../documents-server.mjs';

const here = fileURLToPath(new URL('.', import.meta.url));
const flags = { grpc: ['-documents-dev-owner'], rest: ['-documents'] };

const gen = buildGenconfig();
let code = 0;
try {
  for (const transport of (process.env.ARUMA_SMOKE_DOCUMENTS_TRANSPORTS || 'grpc,rest').split(',').map((s) => s.trim())) {
    if (!flags[transport]) throw new Error(`unknown transport ${transport}`);
    console.log(`\n--- firebase-admin Firestore against Aruma, ${transport}`);
    const server = await startServer(gen.bin, flags[transport]);
    try {
      const c = await runGuarded(join(here, 'documents.test.mjs'), here, {
        ARUMA_SMOKE_DIR: server.dir,
        ARUMA_SMOKE_DOCUMENTS_ADDR: server.addr,
        ARUMA_SMOKE_DOCUMENTS_TRANSPORT: transport,
        ARUMA_NETGUARD_LOG: join(server.dir, 'netguard.log'),
      });
      code ||= c;
      // Listen is gRPC whatever the transport, so its suite runs once.
      if (transport === 'grpc') {
        code ||= await runGuarded(join(here, 'listen.test.mjs'), here, {
          ARUMA_SMOKE_DIR: server.dir,
          ARUMA_SMOKE_DOCUMENTS_ADDR: server.addr,
          ARUMA_NETGUARD_LOG: join(server.dir, 'netguard.log'),
        });
      }
    } finally {
      await server.stop();
    }
  }
} finally {
  gen.remove();
}
process.exit(code);
