<script lang="ts">
  import { app } from './store.svelte'

  // 上から見た会場。ステージ中央が原点、x は右、y は客席側(下)。
  // 表示のみ(座席・スピーカーのドラッグ操作は M3)。
  const preset = $derived(app.venues.find((v) => v.id === app.proj?.venue.preset))

  const STAGE_DEPTH = 6 // 描画用のステージの奥行き(m)
  const view = $derived.by(() => {
    const w = preset?.widthM ?? 80
    const d = preset?.depthM ?? 70
    const pad = Math.max(w, d) * 0.04
    return { x: -w / 2 - pad, y: -STAGE_DEPTH - pad, w: w + pad * 2, h: d + STAGE_DEPTH + pad * 2 }
  })
  const unit = $derived(Math.max(view.w, view.h) / 60) // 図形の基準サイズ(m)
  const stageW = $derived(Math.min((preset?.widthM ?? 80) * 0.5, 30))
</script>

<section>
  <h2>会場マップ</h2>
  {#if app.proj && preset}
    <div class="box"><svg viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label="会場の上面図">
      <rect x={-preset.widthM / 2} y={0} width={preset.widthM} height={preset.depthM} class="floor" />
      <rect x={-stageW / 2} y={-STAGE_DEPTH} width={stageW} height={STAGE_DEPTH} class="stage" />
      <text x="0" y={-STAGE_DEPTH / 2} class="label" font-size={unit * 1.6}>STAGE</text>

      {#each app.proj.venue.speakers as s (s.id)}
        <rect x={s.x - unit} y={s.y - unit * 1.4} width={unit * 2} height={unit * 2.8} class="speaker" />
        <text x={s.x} y={s.y - unit * 2} class="label" font-size={unit * 1.4}>{s.id}</text>
      {/each}

      <!-- リスナー: 向き(yaw 0 でステージ方向 = 画面の上)を三角で示す -->
      <g transform={`translate(${app.proj.listener.x} ${app.proj.listener.y}) rotate(${app.proj.listener.yawDeg})`}>
        <circle r={unit * 1.3} class="listener" />
        <path d={`M0 ${-unit * 2.6} L${unit} ${-unit * 1.2} L${-unit} ${-unit * 1.2} Z`} class="listener" />
      </g>
    </svg></div>
  {/if}
</section>

<style>
  section { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .box { flex: 1; min-height: 0; position: relative; }
  svg { position: absolute; inset: 0; width: 100%; height: 100%; }
  .floor { fill: var(--panel-2); stroke: var(--line); stroke-width: 0.3; }
  .stage { fill: #3a3f4d; stroke: var(--muted); stroke-width: 0.2; }
  .speaker { fill: var(--accent-dim); stroke: var(--accent); stroke-width: 0.2; }
  .listener { fill: var(--warn); }
  .label { fill: var(--muted); text-anchor: middle; dominant-baseline: middle; }
</style>
