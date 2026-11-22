<script lang="ts">
  import { heldHost } from './host';

  // The Spaghetti tab on a printer's page. A device-tab is handed the device
  // id and nothing else.
  interface Props {
    deviceId: string;
  }
  let { deviceId }: Props = $props();

  // The bundle is served under /api/v1/modules/<namespace>/ui/, so the
  // namespace is read from the bundle's own address.
  const ns = new URL(import.meta.url).pathname.split('/')[4];
  const mount = `/api/v1/modules/${ns}`;
  // Host API calls (cameras list) go through the host's own client, with the
  // signed-in person's session and abort signal.
  const hostClient = () => heldHost().client;
  const hostSignal = () => heldHost().signal;

  interface Settings {
    enabled: boolean;
    pause_on_failure: boolean;
    min_interval_seconds: number;
    detect_only_printing: boolean;
    alert_threshold: number;
    pause_threshold: number;
    threshold: number;
    smoothing: number;
    model: string;
    tile_size_px: number;
    tile_overlap: number;
  }
  interface HistoryEntry {
    taken: string;
    printer: string;
    camera: string;
    max_confidence: number;
    smoothed: number;
    detections: number;
    alerting: boolean;
    frame?: string;
    snapshot: string;
  }

  let settings = $state<Settings | null>(null);
  let model = $state('…');
  let modelSel = $state('printguard');
  let printer = $state<any>(null);
  let history = $state<HistoryEntry[]>([]);
  let error = $state('');
  let feeder = $state<any>(null);
  let camName = $state('');
  let cameras = $state<string[]>([]);
  let camConfigs = $state<Record<string, Record<string, unknown>>>({});
  let printerSettings = $state<any>(null);

  async function load() {
    try {
      const [s, st, h, f, cams, camSets, ps] = await Promise.all([
        fetch(`${mount}/settings`).then((r) => r.json()),
        fetch(`${mount}/status?printer=${encodeURIComponent(deviceId)}`).then((r) => r.json()),
        fetch(`${mount}/history?printer=${encodeURIComponent(deviceId)}`).then((r) => r.json()),
        fetch(`${mount}/feeders?printer=${encodeURIComponent(deviceId)}`).then((r) => r.json()),
        // The cameras registered on this instance for the printer, and their
        // registered config (stream URL), via the host client (modules cannot
        // list cameras over the wire).
        hostClient().listCameras({ query: { device: deviceId } }, hostSignal()),
        hostClient().listCameraSettings({}, hostSignal()),
        fetch(`${mount}/printer-settings?printer=${encodeURIComponent(deviceId)}`).then((r) => r.json()),
      ]);
      settings = s;
      model = st.model ?? 'unknown';
      modelSel = s.model ?? 'printguard';
      printer = st.printers?.[deviceId] ?? null;
      history = h.items ?? [];
      feeder = f.feeders?.[deviceId] ?? null;
      printerSettings = ps;
      cameras = (cams.items ?? []).map((c: any) => c.name);
      camConfigs = Object.fromEntries(
        (camSets.items ?? []).map((c: any) => [c.name, c.config ?? {}]),
      );
      if (feeder) {
        camName = feeder.camera ?? '';
      }
      error = '';
    } catch (e: any) {
      error = String(e);
    }
  }

  // The RTSP stream of a camera, read from the camera's own registered config
  // (stream_url for generic cameras, url for rtsp cameras) — the user never
  // types a URL.
  const streamOf = (name: string): string => {
    const c = camConfigs[name] ?? {};
    return (c.stream_url ?? c.url ?? '') as string;
  };

  async function saveFeeder() {
    if (!camName) return;
    const r = await fetch(`${mount}/feeders`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ printer: deviceId, camera: camName, rtsp_url: streamOf(camName) }),
    });
    if (!r.ok) error = `feeder refused: ${await r.text()}`;
    await load();
  }

  async function patch(p: Record<string, unknown>) {
    const r = await fetch(`${mount}/settings`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      // name the printer so the module can check the caller may operate it
      body: JSON.stringify({ printer: deviceId, ...p }),
    });
    if (!r.ok) error = `settings refused: ${await r.text()}`;
    await load();
  }

  $effect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  });

  // A frame is FAIL iff its smoothed score reaches the warn (alert) threshold
  // currently chosen on this page — judged at viewing time.
  const verdict = (e: HistoryEntry) =>
    e.smoothed >= (settings?.alert_threshold ?? 0.2) ? 'FAIL' : 'pass';

  // The grant command, constructed for THIS server and THIS printer. The
  // token is a placeholder: API tokens are shown only once at creation.
  const grantCmd = `curl -k -X POST "${window.location.origin}/api/v1/grants/device/${deviceId}" \\\n  -H "Authorization: Bearer <pcl_YOUR_TOKEN>" \\\n  -H 'Content-Type: application/json' \\\n  -d '{"relation":"operator","subject":"sink:failure-detector"}'`;
</script>

<div class="spaghetti">
  <h2>Spaghetti detection</h2>
  {#if error}<p class="err">{error}</p>{/if}
  {#if settings?.pause_on_failure && printer && printer.can_operate === false}
    <p class="err">
      Pause on failure is ON but this module may not <strong>operate</strong> this printer, so it
      cannot pause prints. An admin must grant it by hand (see the “Operator permission” page).
      For this printer, run:
    </p>
    <pre class="cmd"><code>{grantCmd}</code></pre>
    <p class="err">
      and fill in <code>&lt;pcl_YOUR_TOKEN&gt;</code> with an API credential you create in the
      web UI under <strong>Settings → API credentials → New credential</strong>.
    </p>
  {/if}

  <div class="row">
    <label title="Global option, also settable on the extension's settings page. Last writer wins.">
      Model (global)
      <select bind:value={modelSel} onchange={() => patch({ model: modelSel })}>
        <option value="obico">obico (default, ~200 MB detector)</option>
        <option value="printguard">printguard (~5 MB, fast on CPU)</option>
      </select>
    </label>
    <label>
      <input
        type="checkbox"
        checked={settings?.enabled ?? true}
        onchange={(e) => patch({ enabled: (e.currentTarget as HTMLInputElement).checked })}
      />
      Detection enabled
    </label>
    <label>
      <input
        type="checkbox"
        checked={settings?.pause_on_failure ?? false}
        onchange={(e) => patch({ pause_on_failure: (e.currentTarget as HTMLInputElement).checked })}
      />
      Pause print on failure
    </label>
    <label title={`Per-printer check interval; supersedes the global default (${printerSettings?.default_interval_seconds ?? settings?.min_interval_seconds ?? 30} s).`}>
      Check every (this printer)
      <input
        class="num"
        type="number"
        min="0"
        step="1"
        value={printerSettings?.interval_seconds ?? printerSettings?.default_interval_seconds ?? settings?.min_interval_seconds ?? 30}
        onchange={(e) => {
          const v = parseInt((e.currentTarget as HTMLInputElement).value, 10);
          if (Number.isFinite(v)) {
            fetch(`${mount}/printer-settings`, {
              method: 'POST',
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ printer: deviceId, interval_seconds: v }),
            }).then(async (r) => {
              if (!r.ok) error = `interval refused: ${await r.text()}`;
              await load();
            });
          }
        }}
      />
      s
    </label>
    <label>
      Alert at smoothed
      <input
        class="num"
        type="number"
        min="0"
        max="1"
        step="0.05"
        value={settings?.alert_threshold ?? 0.5}
        onchange={(e) => {
          const v = parseFloat((e.currentTarget as HTMLInputElement).value);
          if (Number.isFinite(v)) patch({ alert_threshold: v });
        }}
      />
    </label>
    <label>
      Pause at smoothed
      <input
        class="num"
        type="number"
        min="0"
        max="1"
        step="0.05"
        value={settings?.pause_threshold ?? 0.4}
        onchange={(e) => {
          const v = parseFloat((e.currentTarget as HTMLInputElement).value);
          if (Number.isFinite(v)) patch({ pause_threshold: v });
        }}
      />
    </label>
  </div>

  <p class="muted">
    Model loaded: {model}
    {#if printer}
      · frames scored: {printer.frames} · detections: {printer.detections} · max confidence:
      {typeof printer.max_confidence === 'number' ? printer.max_confidence.toFixed(3) : '–'} ·
      smoothed: {typeof printer.smoothed === 'number' ? printer.smoothed.toFixed(3) : '–'}
      {#if printer.alerting}· <strong class="fail">ALERT</strong>{/if}
      {#if printer.paused_by_module}· paused by module{/if}
    {:else}
      · no frames scored for this printer yet
    {/if}
  </p>

  <h3>Camera (this printer)</h3>
  <p class="muted">
    The camera registered on this instance for the printer, as configured in Connect Local.
    {#if feeder}<span class="ok">active → {feeder.camera}</span>{:else}no camera selected — frames won't flow until you pick one.{/if}
  </p>
  <div class="row">
    <label>
      Camera
      <select bind:value={camName} onchange={saveFeeder}>
        {#if cameras.length === 0}<option value="">no cameras registered for this printer</option>{/if}
        {#each cameras as c (c)}
          <option value={c}>{c}</option>
        {/each}
        {#if camName && !cameras.includes(camName)}<option value={camName}>{camName} (saved)</option>{/if}
      </select>
    </label>
  </div>

  <h3>History</h3>
  {#if history.length === 0}
    <p class="muted">No scored frames yet. Start a print with the camera attached.</p>
  {:else}
    <div class="grid">
      {#each history as e, i (i)}
        <figure class={verdict(e) === 'FAIL' ? 'fail' : ''}>
          {#if e.frame}
            <img src={`${mount}/${e.frame}`} alt={e.taken} loading="lazy" />
          {/if}
          <figcaption>
            <span class="badge">{verdict(e)}</span>
            {new Date(e.taken).toLocaleString()} · conf {e.max_confidence.toFixed(2)} ·
            {e.detections} det
          </figcaption>
        </figure>
      {/each}
    </div>
  {/if}
</div>

<style>
  .spaghetti {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    padding: 1rem;
    font-size: 0.875rem;
  }
  h2 {
    font-size: 1rem;
    font-weight: 600;
  }
  h3 {
    font-weight: 600;
    margin-top: 0.5rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 1.25rem;
    align-items: center;
  }
  .row label {
    display: flex;
    gap: 0.4rem;
    align-items: center;
  }
  .num {
    width: 4.5rem;
  }
  .muted {
    color: #888;
  }
  .err {
    color: #c00;
  }
  .fail {
    color: #c00;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
    gap: 0.75rem;
  }
  figure {
    margin: 0;
    border: 1px solid #444;
    border-radius: 6px;
    overflow: hidden;
  }
  figure.fail {
    border-color: #c00;
  }
  img {
    width: 100%;
    height: 120px;
    object-fit: cover;
    display: block;
  }
  figcaption {
    padding: 0.3rem 0.5rem;
    font-size: 0.75rem;
    color: #aaa;
  }
  .badge {
    font-weight: 700;
    color: #2a2;
  }
  figure.fail .badge {
    color: #e33;
  }
  pre.cmd {
    background: #0001;
    padding: 0.4rem 0.6rem;
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-all;
    font-size: 0.75rem;
  }
  pre.cmd code {
    background: none;
    padding: 0;
  }
  .ok {
    color: #2a2;
  }
</style>
