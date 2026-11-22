<script lang="ts">
  // The Guide page: a compact how-it-works for a new user, in the extension's
  // own sidebar entry. No props: the route is the whole context.
</script>

<div class="page">
  <h1>Failure Detector — guide</h1>

  <blockquote class="warn">
    <strong>Warning: this project was 100% written by Mistral Vibe.</strong> I have tried to
    direct it and test as best I can, but consider this potential AI slop and take your
    precautions. I did it because it was fun, but it should not be shared without this
    disclaimer. However, it works on my machine!
  </blockquote>

  <h2>How it works</h2>
  <ol>
    <li>While a printer is printing, camera frames are stored by Connect Local (the camera's
      own <code>snapshot_every</code> cadence; the extension's built-in feeder can also pull
      an RTSP camera with the server's <strong>ffmpeg</strong>).</li>
    <li>Each stored frame is scored by an <strong>on-device AI model</strong>, chosen on the
      extension's settings page or the Spaghetti tab:
      <ul>
        <li><strong>obico</strong> (default) — the AGPL-3.0 reference detector, ~200 MB</li>
        <li><strong>printguard</strong> — a ~5 MB ShuffleNetV2 encoder + prototype classifier,
          fast on CPU</li>
      </ul>
      The model file is downloaded once, sha256-verified, and runs in-process (pure Go, no
      external runtime).</li>
    <li>Scores are smoothed (EWMA) per printer. At or above the alert threshold (default 0.2)
      a <code>failure.alert</code> event is published; at or above the pause threshold
      (default 0.4) the print is paused — <em>only if the operator grant exists</em>
      (see the “Operator permission” page).</li>
    <li>The Spaghetti tab on each printer's page shows the last 10 scored frames with photos,
      lets you tune thresholds and the check interval, and shows live statistics.</li>
  </ol>

  <h2>Credits</h2>
  <ul>
    <li>Detection model “obico”: <a href="https://github.com/TheSpaghettiDetective/TheSpaghettiDetective">TheSpaghettiDetective / Obico</a> (AGPL-3.0)</li>
    <li>Detection model “printguard”: <a href="https://github.com/oliverbravery/PrintGuard">oliverbravery/PrintGuard</a> (GPL-2.0)</li>
  </ul>

  <h2>Requirements</h2>
  <ul>
    <li>Cameras already registered on this Connect Local instance need nothing extra.</li>
    <li>RTSP-only cameras are pulled with <strong>ffmpeg installed on the server</strong>
      (e.g. <code>apt install ffmpeg</code>); only <code>rtsp://</code>/<code>rtsps://</code>
      URLs are accepted.</li>
  </ul>
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
  .warn {
    border-left: 3px solid #c90;
    background: #fff7e6;
    padding: 0.5rem 0.75rem;
  }
  code {
    background: #0001;
    padding: 0 0.25rem;
  }
</style>
