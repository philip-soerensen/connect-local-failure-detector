<script lang="ts">
  import { onMount } from 'svelte';
  import { heldHost } from './host';

  // The Operator permission page: why the grant is needed and the exact curl
  // to run. The command is constructed from what the page can know — the
  // server origin (this page's own address), the printer ids (listed from the
  // host) and the module's instance name (sink:failure-detector, from
  // module.yaml's `name`). The API token cannot be read back by anyone, so it
  // stays a placeholder with instructions.
  const origin = window.location.origin;
  // The grant subject is the module instance as kind:name; the namespace is
  // not part of a grant subject, so this is the same on every install.
  const subject = 'sink:failure-detector';

  let printers = $state<{ id: string; name: string }[]>([]);
  let error = $state('');
  let copied = $state('');

  onMount(() => {
    heldHost()
      .client.listDevices({}, heldHost().signal)
      .then((d: any) => {
        printers = (d.items ?? [])
          .filter((x: any) => x.kind === 'printer' || !x.kind)
          .map((x: any) => ({ id: x.id, name: x.name ?? x.id }));
      })
      .catch((e: any) => {
        error = String(e);
      });
  });

  const grantCmd = (id: string) =>
    `curl -k -X POST "${origin}/api/v1/grants/device/${id}" \\\n  -H "Authorization: Bearer <pcl_YOUR_TOKEN>" \\\n  -H 'Content-Type: application/json' \\\n  -d '{"relation":"operator","subject":"${subject}"}'`;

  async function copy(text: string, which: string) {
    try {
      await navigator.clipboard.writeText(text);
      copied = which;
      setTimeout(() => (copied = ''), 1500);
    } catch {
      /* clipboard may be unavailable; the command is selectable anyway */
    }
  }
</script>

<div class="page">
  <h1>Operator permission</h1>

  <p>
    The extension runs as the module instance <code>{subject}</code>. It can detect failures
    and publish alerts on its own, but <strong>pausing a print needs the <code>operate</code>
    permission on that printer, and a module cannot grant that to itself</strong> — an
    administrator writes the grant by hand, once per printer. Until then the Spaghetti tab
    shows a banner and the module can alert but not pause.
  </p>

  <h2>1. Create an API credential</h2>
  <ol>
    <li>Sign in to this Connect Local web UI at <code>{origin}</code>.</li>
    <li>In the left-hand menu, open <strong>Settings → API credentials</strong> and click
      <strong>New credential</strong>. The credential looks like <code>pcl_…</code> and is
      <strong>shown only once</strong> — copy it now. It is the
      <code>&lt;pcl_YOUR_TOKEN&gt;</code> placeholder below; nobody (not even this page) can
      read it back.</li>
  </ol>

  <h2>2. Grant the module the operator relation on each printer</h2>
  <p>
    Run one command per printer from a terminal that can reach the server
    (<code>-k</code> because Connect Local serves a self-signed certificate):
  </p>

  {#if error}<p class="err">could not list printers: {error} — use the placeholder command below</p>{/if}
  {#each printers as p (p.id)}
    <div class="printer">
      <strong>{p.name}</strong> <span class="muted">({p.id})</span>
      <pre><code>{grantCmd(p.id)}</code></pre>
      <button onclick={() => copy(grantCmd(p.id), p.id)}>{copied === p.id ? 'copied!' : 'copy'}</button>
    </div>
  {/each}
  {#if printers.length === 0 && !error}
    <div class="printer">
      <pre><code>{grantCmd('<printer-id>')}</code></pre>
      <p class="muted">
        Replace <code>&lt;printer-id&gt;</code> with the printer's id — find it in the printer
        page URL or with <code>curl -k -H "Authorization: Bearer &lt;pcl_YOUR_TOKEN&gt;" "{origin}/api/v1/devices"</code>.
      </p>
    </div>
  {/if}

  <p>
    A <code>201</code> response means the grant is written. The Spaghetti tab on the printer's
    page then stops showing the banner and the module may pause prints when the pause threshold
    is crossed. To revoke: the same URL with <code>DELETE</code> and
    <code>?relation=operator&amp;subject={subject}</code> as query parameters.
  </p>
</div>

<style>
  .page {
    max-width: 46rem;
    padding: 1rem 1.5rem;
    font-size: 0.875rem;
    line-height: 1.45;
  }
  h1 {
    font-size: 1.15rem;
  }
  h2 {
    font-size: 1rem;
    margin-top: 1rem;
  }
  pre {
    background: rgb(128 128 128 / 0.14);
    padding: 0.5rem 0.75rem;
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-all;
  }
  code {
    background: rgb(128 128 128 / 0.18);
    padding: 0 0.25rem;
  }
  pre code {
    padding: 0;
    background: none;
  }
  .printer {
    margin: 0.75rem 0;
  }
  .muted {
    color: color-mix(in srgb, currentColor 55%, transparent);
  }
  .err {
    color: #e0342f;
  }
  a {
    color: inherit;
  }
  button {
    font: inherit;
  }
</style>
