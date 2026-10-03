<script lang="ts">
  import { app } from './store.svelte'
  import { player, VOLUME_MAX_DB, VOLUME_MIN_DB } from './player.svelte'
  import { mmss } from './format'

  const hasPreview = $derived(player.loaded.processed)
  const total = $derived(player.duration || app.duration)

  // スペースキーで再生/停止(入力欄・ボタン・選択欄の操作中は除く)
  function onKey(e: KeyboardEvent) {
    if (e.code !== 'Space' || !hasPreview) return
    const t = e.target as HTMLElement | null
    if (t && ['INPUT', 'SELECT', 'TEXTAREA', 'BUTTON'].includes(t.tagName)) return
    e.preventDefault()
    void player.toggle()
  }
</script>

<svelte:window onkeydown={onKey} />

<div class="transport">
  <button class="primary play" disabled={!hasPreview} onclick={() => player.toggle()} aria-label={player.playing ? '一時停止' : '再生'}>
    {player.playing ? '⏸ 停止' : '▶ 再生'}
  </button>
  <button disabled={!hasPreview} onclick={() => player.seek(0)} title="曲の頭に戻る" aria-label="曲の頭に戻る">⏮</button>

  <div class="ab" role="group" aria-label="A/B切り替え">
    <button class:active={player.mode === 'original'} disabled={!player.loaded.original} onclick={() => player.setMode('original')}>A 原音</button>
    <button class:active={player.mode === 'processed'} disabled={!player.loaded.processed} onclick={() => player.setMode('processed')}>B 加工後</button>
  </div>

  <div class="volume" class:hot={player.volumeDb > 0 && !player.muted} title="再生音量。聞こえ方だけを変えるので再計算は不要です(プレビュー専用で、書き出しには反映されません)。ダブルクリックで 0 dB">
    <button class="icon" onclick={() => player.toggleMute()} aria-pressed={player.muted} aria-label="ミュート">{player.muted ? '🔇' : '🔊'}</button>
    <input
      type="range"
      min={VOLUME_MIN_DB}
      max={VOLUME_MAX_DB}
      step="0.5"
      value={player.volumeDb}
      oninput={(e) => player.setVolume(parseFloat(e.currentTarget.value))}
      ondblclick={() => player.setVolume(0)}
      aria-label="再生音量(dB)"
    />
    <span class="vol">{player.muted ? 'ミュート' : `${player.volumeDb > 0 ? '+' : ''}${player.volumeDb.toFixed(1)} dB`}</span>
  </div>

  <span class="pos">{mmss(player.position)} / {mmss(total)}</span>
  <span class="hint">波形をクリック・ドラッグで再生位置を移動 / Space で再生・停止 / 音量は再計算なしで変わります(プレビュー専用)</span>

  <div class="status" aria-live="polite">
    {#if app.busy > 0}<span class="spin"></span> 曲全体を処理中…{#if player.windowed}<span class="early">(先行プレビュー: この位置から約30秒を再生できます)</span>{/if}{:else if hasPreview}<span class="ok">● 最新</span>{/if}
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
  .volume { display: flex; align-items: center; gap: 6px; }
  .volume input { width: 110px; }
  .vol { min-width: 5.5em; font-variant-numeric: tabular-nums; color: var(--muted); }
  .volume.hot .vol { color: var(--warn); }
  .pos { font-variant-numeric: tabular-nums; color: var(--text); }
  .hint { color: var(--muted); font-size: 11px; }
  .status { margin-left: auto; color: var(--muted); display: flex; align-items: center; gap: 6px; }
  .ok { color: var(--ok); }
  .early { color: var(--muted); font-size: 11px; }
  .spin { width: 10px; height: 10px; border: 2px solid var(--line); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spin { animation: none; } }
</style>
