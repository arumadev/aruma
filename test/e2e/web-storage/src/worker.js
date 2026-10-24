// A Web Worker of the app (new Worker(new URL('./worker.js', import.meta.url))): bundlers build it
// apart from the page (Vite with worker.plugins alone), so it is where a rewrite can go missing. It
// uploads, reads the link and deletes under open/ (no user signs in inside the worker) and reports
// its Storage host and the link.
import { initializeApp } from 'firebase/app';
import * as st from 'firebase/storage';

self.onmessage = async ({ data }) => {
  try {
    const storage = st.getStorage(initializeApp(data.config, 'worker'));
    storage.maxOperationRetryTime = 8000;
    storage.maxUploadRetryTime = 8000;
    const r = st.ref(storage, `open/worker-${data.bundler}-${Date.now()}.txt`);
    await st.uploadBytes(r, new TextEncoder().encode('from a worker\n'), { contentType: 'text/plain' });
    const url = await st.getDownloadURL(r);
    await st.deleteObject(r);
    self.postMessage({ ok: true, host: storage.host, url });
  } catch (e) {
    self.postMessage({ ok: false, error: String(e?.message ?? e) });
  }
};
