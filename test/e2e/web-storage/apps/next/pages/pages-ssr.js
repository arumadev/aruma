// The Pages Router: getServerSideProps runs in the server bundle, where Next.js leaves node_modules
// external unless told otherwise. The link it reads is rendered into the HTML (measure.sh checks it).
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { getApps, initializeApp } from 'firebase/app';
import { getDownloadURL, getStorage, ref } from 'firebase/storage';

export async function getServerSideProps() {
  const e2e = JSON.parse(readFileSync(join(process.env.ARUMA_E2E_DIR, 'e2e.json'), 'utf8'));
  const app = getApps().find((a) => a.name === 'pages') ?? initializeApp(e2e.config, 'pages');
  const storage = getStorage(app);
  const url = await getDownloadURL(ref(storage, e2e.ssr));
  return { props: { url, host: `${storage._protocol}://${storage.host}` } };
}

export default function PagesSSR({ url, host }) {
  return (
    <main>
      <p id="ssr-host">{host}</p>
      <img id="ssr" src={url} alt="rendered by getServerSideProps" />
    </main>
  );
}
