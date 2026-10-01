<script lang="ts">
  import { app } from './store.svelte'
  import { clamp } from './format'

  // 上から見た会場。ステージ中央が原点、x は右、y は客席側(下)。
  // 座席(リスナー)とスピーカーをドラッグで動かし、リスナーの向きは矢印の先のつまみで変える。
  // 矢印キーでも動かせる(Shiftで大きく)。
  const preset = $derived(app.venues.find((v) => v.id === app.proj?.venue.preset))

  const STAGE_DEPTH = 6 // 描画用のステージの奥行き(m)
  const SNAP = 0.5 // 位置の刻み(m)
  const YAW_SNAP = 5 // 向きの刻み(度)

  const view = $derived.by(() => {
    const w = preset?.widthM ?? 80
    const d = preset?.depthM ?? 70
    const pad = Math.max(w, d) * 0.04
    return { x: -w / 2 - pad, y: -STAGE_DEPTH - pad, w: w + pad * 2, h: d + STAGE_DEPTH + pad * 2 }
  })
  const unit = $derived(Math.max(view.w, view.h) / 60) // 図形の基準サイズ(m)
  const stageW = $derived(Math.min((preset?.widthM ?? 80) * 0.5, 30))
  const snap = (v: number, step = SNAP) => Math.round(v / step) * step

  let svg = $state<SVGSVGElement>()
  type Target = { kind: 'listener' } | { kind: 'speaker'; index: number } | { kind: 'yaw' }
  let drag = $state<Target | null>(null)
  let tip = $state('')

  /** 画面上の位置を会場の座標(m)に変換する。 */
  function toWorld(e: PointerEvent): { x: number; y: number } {
    const pt = svg!.createSVGPoint()
    pt.x = e.clientX
    pt.y = e.clientY
    const p = pt.matrixTransform(svg!.getScreenCTM()!.inverse())
    return { x: p.x, y: p.y }
  }

  function moveListener(x: number, y: number) {
    if (!app.proj || !preset) return
    app.proj.listener.x = clamp(snap(x), -preset.widthM / 2, preset.widthM / 2)
    app.proj.listener.y = clamp(snap(y), 0, preset.depthM)
  }

  function moveSpeaker(i: number, x: number, y: number) {
    if (!app.proj || !preset) return
    const s = app.proj.venue.speakers[i]
    s.x = clamp(snap(x), -preset.widthM / 2, preset.widthM / 2)
    s.y = clamp(snap(y), -STAGE_DEPTH, preset.depthM)
  }

  function start(e: PointerEvent, target: Target) {
    drag = target
    ;(e.currentTarget as Element).setPointerCapture(e.pointerId)
    e.preventDefault()
  }

  function move(e: PointerEvent) {
    if (!drag || !app.proj) return
    const { x, y } = toWorld(e)
    if (drag.kind === 'listener') moveListener(x, y)
    else if (drag.kind === 'speaker') moveSpeaker(drag.index, x, y)
    else {
      const l = app.proj.listener
      const deg = (Math.atan2(x - l.x, -(y - l.y)) * 180) / Math.PI
      app.proj.listener.yawDeg = snap(deg, YAW_SNAP) % 360
    }
  }

  function end() {
    drag = null
  }

  function key(e: KeyboardEvent, target: Target) {
    if (!app.proj) return
    const d = ({ ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] } as Record<string, number[]>)[e.key]
    if (!d) return
    e.preventDefault()
    const step = e.shiftKey ? 5 : SNAP
    if (target.kind === 'listener') {
      moveListener(app.proj.listener.x + d[0] * step, app.proj.listener.y + d[1] * step)
    } else if (target.kind === 'speaker') {
      const s = app.proj.venue.speakers[target.index]
      moveSpeaker(target.index, s.x + d[0] * step, s.y + d[1] * step)
    } else {
      app.proj.listener.yawDeg = (app.proj.listener.yawDeg + d[0] * (e.shiftKey ? 45 : YAW_SNAP) + 360) % 360
    }
  }

  const yawRad = $derived(((app.proj?.listener.yawDeg ?? 0) * Math.PI) / 180)
  const handle = $derived({
    x: (app.proj?.listener.x ?? 0) + Math.sin(yawRad) * unit * 5,
    y: (app.proj?.listener.y ?? 0) - Math.cos(yawRad) * unit * 5,
  })

  const where = (x: number, y: number) => `x ${x.toFixed(1)} m, y ${y.toFixed(1)} m`
</script>

<section>
  <h2>会場マップ <span class="tip">{tip || 'ドラッグで座席・スピーカーを移動、矢印の先のつまみで向きを変更'}</span></h2>
  {#if app.proj && preset}
    <div class="box">
      <svg
        bind:this={svg}
        viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
        preserveAspectRatio="xMidYMid meet"
        role="group"
        aria-label="会場の上面図"
        onpointermove={move}
        onpointerup={end}
        onpointercancel={end}
      >
        <rect x={-preset.widthM / 2} y={0} width={preset.widthM} height={preset.depthM} class="floor" />
        <rect x={-stageW / 2} y={-STAGE_DEPTH} width={stageW} height={STAGE_DEPTH} class="stage" />
        <text x="0" y={-STAGE_DEPTH / 2} class="label" font-size={unit * 1.6}>STAGE</text>

        {#each app.proj.venue.speakers as s, i (s.id)}
          <g
            class="speaker"
            class:active={drag?.kind === 'speaker' && drag.index === i}
            role="slider"
            tabindex="0"
            aria-label={`スピーカー ${s.id}`}
            aria-valuetext={where(s.x, s.y)}
            aria-valuenow={s.x}
            onpointerdown={(e) => start(e, { kind: 'speaker', index: i })}
            onkeydown={(e) => key(e, { kind: 'speaker', index: i })}
            onpointerenter={() => (tip = `スピーカー ${s.id}: ${where(s.x, s.y)}`)}
            onpointerleave={() => (tip = '')}
          >
            <rect x={s.x - unit * 1.2} y={s.y - unit * 1.6} width={unit * 2.4} height={unit * 3.2} />
            <text x={s.x} y={s.y} class="spk-label" font-size={unit * 1.5}>{s.id}</text>
          </g>
        {/each}

        <!-- 向きのつまみ(yaw 0 でステージ方向 = 画面の上) -->
        <line x1={app.proj.listener.x} y1={app.proj.listener.y} x2={handle.x} y2={handle.y} class="aim" stroke-width={unit * 0.25} />
        <circle
          cx={handle.x}
          cy={handle.y}
          r={unit * 1.1}
          class="yaw"
          class:active={drag?.kind === 'yaw'}
          role="slider"
          tabindex="0"
          aria-label="リスナーの向き"
          aria-valuenow={app.proj.listener.yawDeg}
          aria-valuemin="0"
          aria-valuemax="360"
          onpointerdown={(e) => start(e, { kind: 'yaw' })}
          onkeydown={(e) => key(e, { kind: 'yaw' })}
          onpointerenter={() => (tip = `向き ${app.proj?.listener.yawDeg}°`)}
          onpointerleave={() => (tip = '')}
        />

        <g
          class="listener"
          class:active={drag?.kind === 'listener'}
          transform={`translate(${app.proj.listener.x} ${app.proj.listener.y}) rotate(${app.proj.listener.yawDeg})`}
          role="slider"
          tabindex="0"
          aria-label="座席(リスナー)"
          aria-valuetext={where(app.proj.listener.x, app.proj.listener.y)}
          aria-valuenow={app.proj.listener.x}
          onpointerdown={(e) => start(e, { kind: 'listener' })}
          onkeydown={(e) => key(e, { kind: 'listener' })}
          onpointerenter={() => (tip = `座席: ${where(app.proj!.listener.x, app.proj!.listener.y)}`)}
          onpointerleave={() => (tip = '')}
        >
          <circle r={unit * 1.6} class="hit" />
          <circle r={unit * 1.3} />
          <path d={`M0 ${-unit * 2.6} L${unit} ${-unit * 1.2} L${-unit} ${-unit * 1.2} Z`} />
        </g>
      </svg>
    </div>
  {/if}
</section>

<style>
  section { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .tip { font-weight: 400; margin-left: 10px; letter-spacing: 0; }
  .box { flex: 1; min-height: 0; position: relative; }
  svg { position: absolute; inset: 0; width: 100%; height: 100%; touch-action: none; }
  .floor { fill: var(--panel-2); stroke: var(--line); stroke-width: 0.3; }
  .stage { fill: #3a3f4d; stroke: var(--muted); stroke-width: 0.2; }
  .label { fill: var(--muted); text-anchor: middle; dominant-baseline: middle; pointer-events: none; }
  .speaker { cursor: grab; }
  .speaker rect { fill: var(--accent-dim); stroke: var(--accent); stroke-width: 0.25; }
  .spk-label { fill: var(--text); text-anchor: middle; dominant-baseline: middle; pointer-events: none; }
  .listener { cursor: grab; fill: var(--warn); }
  .listener .hit { fill: transparent; }
  .aim { stroke: var(--warn); opacity: 0.5; pointer-events: none; }
  .yaw { fill: var(--bg); stroke: var(--warn); stroke-width: 0.3; cursor: grab; }
  .active, .active rect { cursor: grabbing; }
  .speaker:focus-visible rect, .yaw:focus-visible, .listener:focus-visible .hit { stroke: var(--text); stroke-width: 0.4; outline: none; }
  .listener:focus-visible .hit { fill: #ffffff22; }
</style>
