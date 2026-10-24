<script>
  // In the browser (the client bundle): the same eleven operations as the other bundles.
  import { onMount } from 'svelte';
  import { install } from '@aruma/web';
  import { initializeApp } from 'firebase/app';
  import { getAuth, signInAnonymously } from 'firebase/auth';
  import * as st from 'firebase/storage';
  import { run } from '../../../../src/ops.js';

  let { data } = $props();

  onMount(() => {
    window.__done = (async () => {
      const { e2e } = data;
      install({ url: e2e.aruma });
      const app = initializeApp(e2e.config);
      const { user } = await signInAnonymously(getAuth(app));
      const results = await run({ storage: st.getStorage(app), st, aruma: e2e.aruma, bucket: e2e.bucket, dir: `e2e/${user.uid}/sveltekit` });
      return { bundler: 'sveltekit', results };
    })().catch((e) => ({ bundler: 'sveltekit', error: String(e?.stack ?? e) }));
  });
</script>

<p id="ssr-host">{data.ssr.protocol}://{data.ssr.host}</p>
<img id="ssr" src={data.ssr.url} alt="rendered on the server" />
