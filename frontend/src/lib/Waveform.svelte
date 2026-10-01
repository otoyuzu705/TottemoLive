<script lang="ts">
  import { app } from './store.svelte'
  import { player } from './player.svelte'
  import { clamp, mmss } from './format'

  // 波形(min/maxピーク列をcanvasに描く)と、再生位置の表示・移動。
  let wrap: HTMLDivElement
  let canvas: HTMLCanvasElement
  let width = $state(0)
  const HEIGHT = 96

  const duration = $derived(app.duration)

  function draw() {
    if (!canvas || width === 0) return
    const dpr = window.devicePixelRatio || 1
    canvas.width = Math.round(width * dpr)
    canvas.height = Math.round(HEIGHT * dpr)
    const g = canvas.getContext('2d')!
    g.scale(dpr, dpr)
    g.clearRect(0, 0, width, HEIGHT)
    const mid = HEIGHT / 2
    g.fillStyle = '#5b9dff99'
    // 全音源のピークを重ねて描く(各ピクセル列で最大の振幅)
    const ids = app.proj?.sources.map((s) => s.id) ?? []
    const list = ids.map((id) => ({ peaks: app.peaks[id], dur: app.infos[id]?.durationSec ?? 0 })).filter((x) => x.peaks)
    const scale = Math.max(0.05, ...list.flatMap((x) => Array.from(x.peaks)).map(Math.abs))
    for (let px = 0; px < width; px++) {
      const t = (px / width) * duration
      let lo = 0
      let hi = 0
      for (const { peaks, dur } of list) {
        if (dur <= 0 || t >= dur) continue
        const n = peaks.length / 2
        const i = Math.min(n - 1, Math.floor((t / dur) * n))
        lo = Math.min(lo, peaks[2 * i])
        hi = Math.max(hi, peaks[2 * i + 1])
      }
      const y0 = mid - (hi / scale) * mid * 0.95
      const y1 = mid - (lo / scale) * mid * 0.95
      g.fillRect(px, y0, 1, Math.max(1, y1 - y0))
    }
  }

  $effect(() => {
    // 依存: ピーク列・幅・曲の長さ
    void [app.peaks, width, duration, app.proj?.sources.length]
    draw()
  })

  $effect(() => {
    const ro = new ResizeObserver(([e]) => (width = Math.floor(e.contentRect.width)))
    ro.observe(wrap)
    return () => ro.disconnect()
  })

  const pct = (sec: number) => (duration > 0 ? clamp((sec / duration) * 100, 0, 100) : 0)

  // クリック・ドラッグで再生位置を動かす
  let scrubbing = false

  function seekTo(e: PointerEvent) {
    const r = wrap.getBoundingClientRect()
    player.seek(clamp(((e.clientX - r.left) / r.width) * duration, 0, duration))
  }

  function down(e: PointerEvent) {
    if (duration <= 0 || !player.loaded.processed) return
    wrap.setPointerCapture(e.pointerId)
    scrubbing = true
    seekTo(e)
  }

  function move(e: PointerEvent) {
    if (scrubbing) seekTo(e)
  }

  function up() {
    scrubbing = false
  }
</script>

<div class="wave" bind:this={wrap} onpointerdown={down} onpointermove={move} onpointerup={up} role="slider" aria-label="再生位置" aria-valuenow={player.position} aria-valuemin="0" aria-valuemax={duration} tabindex="-1">
  <canvas bind:this={canvas} style={`width:${width}px;height:${HEIGHT}px`}></canvas>
  {#if duration > 0}
    <div class="played" style={`width:${pct(player.position)}%`}></div>
    <div class="playhead" style={`left:${pct(player.position)}%`} class:on={player.loaded.processed || player.loaded.original}></div>
    <div class="time">{mmss(0)}</div>
    <div class="time end">{mmss(duration)}</div>
  {:else}
    <div class="hint">音源を追加すると波形が表示されます</div>
  {/if}
</div>

<style>
  .wave { position: relative; height: 96px; background: var(--panel); border: 1px solid var(--line); border-radius: 6px; overflow: hidden; touch-action: none; cursor: crosshair; }
  canvas { display: block; }
  .played { position: absolute; top: 0; bottom: 0; left: 0; background: #5b9dff22; pointer-events: none; }
  .playhead { position: absolute; top: 0; bottom: 0; width: 2px; background: var(--warn); pointer-events: none; display: none; }
  .playhead.on { display: block; }
  .time { position: absolute; bottom: 2px; left: 4px; font-size: 10px; color: var(--muted); pointer-events: none; }
  .time.end { left: auto; right: 4px; }
  .hint { position: absolute; inset: 0; display: grid; place-items: center; color: var(--muted); }
</style>
