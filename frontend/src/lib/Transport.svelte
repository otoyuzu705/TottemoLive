<script lang="ts">
  import { app, MAX_PREVIEW_SEC } from './store.svelte'
  import { player } from './player.svelte'
  import { clamp, mmss } from './format'

  function setStart(text: string) {
    const x = parseFloat(text)
    if (Number.isFinite(x)) app.region.start = clamp(x, 0, Math.max(0, app.duration - 1))
  }

  function setLen(text: string) {
    const x = parseFloat(text)
    if (Number.isFinite(x)) app.region.len = clamp(x, 1, MAX_PREVIEW_SEC)
  }

  const hasPreview = $derived(player.loaded.processed)
</script>

<div class="transport">
  <button class="primary play" disabled={!hasPreview} onclick={() => player.toggle()} aria-label={player.playing ? '一時停止' : '再生'}>
    {player.playing ? '⏸ 停止' : '▶ 再生'}
  </button>

  <div class="ab" role="group" aria-label="A/B切り替え">
    <button class:active={player.mode === 'original'} disabled={!player.loaded.original} onclick={() => player.setMode('original')}>A 原音</button>
    <button class:active={player.mode === 'processed'} disabled={!player.loaded.processed} onclick={() => player.setMode('processed')}>B 加工後</button>
  </div>

  <div class="region">
    <label>開始<input type="number" min="0" step="1" value={app.region.start.toFixed(1)} onchange={(e) => setStart(e.currentTarget.value)} />秒</label>
    <label>長さ<input type="number" min="1" max={MAX_PREVIEW_SEC} step="1" value={app.region.len.toFixed(1)} onchange={(e) => setLen(e.currentTarget.value)} />秒</label>
    <span class="pos">{mmss(app.region.start + player.position)} / {mmss(app.duration)}</span>
  </div>

  <div class="status" aria-live="polite">
    {#if app.busy > 0}<span class="spin"></span> プレビューを作成中…{:else if hasPreview}<span class="ok">● 最新</span>{/if}
  </div>
</div>

<style>
  .transport { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
  .play { min-width: 6.5em; }
  .ab { display: flex; }
  .ab button { border-radius: 0; }
  .ab button:first-child { border-radius: 5px 0 0 5px; }
  .ab button:last-child { border-radius: 0 5px 5px 0; margin-left: -1px; }
  .ab button.active { background: var(--accent-dim); border-color: var(--accent); }
  .region { display: flex; align-items: center; gap: 10px; color: var(--muted); }
  .region label { display: flex; align-items: center; gap: 4px; }
  .region input { width: 6ch; }
  .pos { font-variant-numeric: tabular-nums; }
  .status { margin-left: auto; color: var(--muted); display: flex; align-items: center; gap: 6px; }
  .ok { color: var(--ok); }
  .spin { width: 10px; height: 10px; border: 2px solid var(--line); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spin { animation: none; } }
</style>
