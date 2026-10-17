// Fails the smoke test on any network activity: the SDKs must accept the config files
// without contacting anything. Shared by the Node-based smoke tests.
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import tls from 'node:tls';
import { syncBuiltinESMExports } from 'node:module';

export const attempts = [];

function block(what) {
  return (...args) => {
    const target = typeof args[0] === 'string' || args[0] instanceof URL ? String(args[0]) : JSON.stringify(args[0]?.host ?? args[0]?.hostname ?? args[0]);
    attempts.push(`${what} ${target}`);
    throw new Error(`network access blocked by the smoke test: ${what} ${target}`);
  };
}

globalThis.fetch = block('fetch');
globalThis.XMLHttpRequest = class { constructor() { block('XMLHttpRequest')('') } };
for (const mod of [http, https]) {
  mod.request = block(mod === http ? 'http.request' : 'https.request');
  mod.get = block(mod === http ? 'http.get' : 'https.get');
}
net.connect = net.createConnection = block('net.connect');
tls.connect = block('tls.connect');
// Every TCP connection ends here, whichever API started it.
net.Socket.prototype.connect = block('socket.connect');
// Make `import { connect } from 'node:net'` and friends see the patched functions too.
syncBuiltinESMExports();
