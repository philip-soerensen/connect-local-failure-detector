<script lang="ts">
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
  let camURL = $state('');

  async function load() {
    try {
      const [s, st, h, f] = await Promise.all([
        fetch(`${mount}/settings`).then((r) => r.json()),
        fetch(`${mount}/status?printer=${encodeURIComponent(deviceId)}`).then((r) => r.json()),
        fetch(`${mount}/history?printer=${encodeURIComponent(deviceId)}`).then((r) => r.json()),
        fetch(`${mount}/feeders?printer=${encodeURIComponent(deviceId)}`).then((r) => r.json()),
      ]);
      settings = s;
      model = st.model ?? 'unknown';
      modelSel = s.model ?? 'printguard';
      printer = st.printers?.[deviceId] ?? null;
      history = h.items ?? [];
      feeder = f.feeders?.[deviceId] ?? null;
      if (feeder) {
        camName = feeder.camera ?? '';
        camURL = feeder.rtsp_url ?? feeder.snapshot_url ?? '';
      }
      error = '';
    } catch (e: any) {
      error = String(e);
    }
  }

  async function saveFeeder() {
    const r = await fetch(`${mount}/feeders`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ printer: deviceId, camera: camName, rtsp_url: camURL }),
    });
    if (!r.ok) error = `feeder refused: ${await r.text()}`;
    await load();
  }

  async function patch(p: Record<string, unknown>) {
    const r = await fetch(`${mount}/settings`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(p),
    });
    if (!r.ok) error = `settings refused: ${await r.text()}`;
    await load();
  }

  $effect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  });

  const verdict = (e: HistoryEntry) => (e.alerting || e.detections > 0 ? 'FAIL' : 'pass');
</script>

<div class="spaghetti">
  <h2>Spaghetti detection</h2>
  {#if error}<p class="err">{error}</p>{/if}
  {#if settings?.pause_on_failure && printer && printer.can_operate === false}
    <p class="err">
      Pause on failure is ON but this module may not <strong>operate</strong> this printer, so it
      cannot pause prints. An admin must grant it by hand:
      <code>POST /api/v1/grants/device/{deviceId} {`{"relation":"operator","subject":"sink:failure-detector"}`}</code>
    </p>
  {/if}

  <div class="row">
    <label>
      Model
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
    <label>
      Check every
      <input
        class="num"
        type="number"
        min="0"
        step="1"
        value={settings?.min_interval_seconds ?? 30}
        onchange={(e) => {
          const v = parseInt((e.currentTarget as HTMLInputElement).value, 10);
          if (Number.isFinite(v)) patch({ min_interval_seconds: v });
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

  <h3>Camera source (this printer)</h3>
  <p class="muted">
    The built-in feeder pushes frames to the printer's camera, so no external pusher is
    needed. RTSP sources (e.g. the standalone Buddy3D) use the server's ffmpeg.
    {#if feeder}<span class="ok">feeder active → {feeder.camera}</span>{:else}no feeder configured — frames won't flow until you set one.{/if}
  </p>
  <div class="row">
    <label>
      Camera
      <input class="num" type="text" placeholder="buddy-cam" bind:value={camName} />
    </label>
    <label>
      RTSP URL
      <input class="num" type="text" placeholder="rtsp://192.168.1.100/live" bind:value={camURL} />
    </label>
    <button onclick={saveFeeder}>Save</button>
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
</style>
