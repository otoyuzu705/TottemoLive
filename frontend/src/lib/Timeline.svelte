<script lang="ts">
  import { app } from './store.svelte'
  import { player } from './player.svelte'
  import { clamp } from './format'

  // 客席タイムライン。歓声の強さ(0〜1)のキーフレームと、手拍子区間を編集する。
  // 時間軸は曲の長さに会場の残響の尾を足したもの(曲の終わりの後の歓声も置ける)。
  // 歓声ツール: 空いた所をクリックで追加 / つまみをドラッグで移動 / ダブルクリックで削除
  // 手拍子ツール: 空いた所をドラッグで区間を作る / 区間の端や中身をドラッグで調整 / ダブルクリックで削除
  type Tool = 'cheer' | 'clap'
  let tool = $state<Tool>('cheer')

  const H = 78
  const CURVE_TOP = 6
  const CURVE_H = 48
  const LANE_Y = 60
  const LANE_H = 14
  const T_SNAP = 0.1
  const V_SNAP = 0.05
  const MIN_CLAP = 0.5
  const EDGE_PX = 6

  let wrap: HTMLDivElement | undefined = $state()
  let width = $state(0)

  const preset = $derived(app.venues.find((v) => v.id === app.proj?.venue.preset))
  const tail = $derived(Math.ceil((preset?.rt60Sec ?? 2) * 1.2))
  const axis = $derived(Math.max(app.duration + tail, 1))
  const enabled = $derived(app.duration > 0)

  const xOf = (t: number) => (t / axis) * width
  const yOf = (v: number) => CURVE_TOP + (1 - v) * CURVE_H
  // 刻みに丸め、浮動小数の端数(24.400000000000002 など)は落とす
  const snapT = (t: number) => clamp(Number((Math.round(t / T_SNAP) * T_SNAP).toFixed(1)), 0, axis)
  const snapV = (v: number) => clamp(Number((Math.round(v / V_SNAP) * V_SNAP).toFixed(2)), 0, 1)

  $effect(() => {
    if (!wrap) return
    const ro = new ResizeObserver(([e]) => (width = Math.floor(e.contentRect.width)))
    ro.observe(wrap)
    return () => ro.disconnect()
  })

  const keyframes = $derived(app.proj?.crowd.keyframes ?? [])
  const ranges = $derived(app.proj?.crowd.clapRanges ?? [])

  // 歓声カーブの折れ線(端の外側は最初・最後の値のまま続く)
  const curve = $derived.by(() => {
    if (keyframes.length === 0) return ''
    const pts = keyframes.map((k) => `${xOf(k.t)},${yOf(k.cheer)}`)
    const first = keyframes[0]
    const last = keyframes[keyframes.length - 1]
    return `${xOf(0)},${yOf(first.cheer)} ${pts.join(' ')} ${xOf(axis)},${yOf(last.cheer)}`
  })

  type Drag =
    | { kind: 'kf'; i: number }
    | { kind: 'clap-new'; t0: number; index: number | null }
    | { kind: 'clap-move'; i: number; offset: number }
    | { kind: 'clap-l'; i: number }
    | { kind: 'clap-r'; i: number }
  let drag: Drag | null = null

  function pos(e: PointerEvent): { t: number; v: number } {
    const r = wrap!.getBoundingClientRect()
    return {
      t: clamp(((e.clientX - r.left) / r.width) * axis, 0, axis),
      v: clamp(1 - (e.clientY - r.top - CURVE_TOP) / CURVE_H, 0, 1),
    }
  }

  function capture(e: PointerEvent) {
    try {
      wrap!.setPointerCapture(e.pointerId)
    } catch {
      // 無効なポインターID(消えたタッチなど)でも編集は続ける。要素の外に出たときの追従だけ効かなくなる
    }
  }

  function downKeyframe(e: PointerEvent, i: number) {
    if (tool !== 'cheer') return
    e.stopPropagation()
    capture(e)
    drag = { kind: 'kf', i }
  }

  function downRange(e: PointerEvent, i: number, part: 'l' | 'r' | 'move') {
    if (tool !== 'clap' || !app.proj) return
    e.stopPropagation()
    capture(e)
    const { t } = pos(e)
    drag = part === 'move' ? { kind: 'clap-move', i, offset: t - app.proj.crowd.clapRanges[i].start } : { kind: part === 'l' ? 'clap-l' : 'clap-r', i }
  }

  function down(e: PointerEvent) {
    if (!enabled || !app.proj) return
    const { t, v } = pos(e)
    const y = e.clientY - wrap!.getBoundingClientRect().top
    if (tool === 'cheer' && y < LANE_Y) {
      // 空いた所のクリックで追加し、そのままドラッグで動かせる
      app.proj.crowd.keyframes.push({ t: snapT(t), cheer: snapV(v) })
      app.proj.crowd.keyframes.sort((a, b) => a.t - b.t)
      const i = app.proj.crowd.keyframes.findIndex((k) => k.t === snapT(t) && k.cheer === snapV(v))
      capture(e)
      drag = { kind: 'kf', i }
    } else if (tool === 'clap') {
      capture(e)
      drag = { kind: 'clap-new', t0: snapT(t), index: null }
    }
  }

  function move(e: PointerEvent) {
    if (!drag || !app.proj) return
    const { t, v } = pos(e)
    const c = app.proj.crowd
    if (drag.kind === 'kf') {
      const k = c.keyframes[drag.i]
      k.t = snapT(t)
      k.cheer = snapV(v)
    } else if (drag.kind === 'clap-new') {
      const a = Math.min(drag.t0, snapT(t))
      const b = Math.max(drag.t0, snapT(t))
      if (b - a >= MIN_CLAP) {
        // 作成中の区間は最初に1つ追加し、動かすたびに更新する
        if (drag.index === null) {
          c.clapRanges.push({ start: a, end: b })
          drag.index = c.clapRanges.length - 1
        } else {
          c.clapRanges[drag.index].start = a
          c.clapRanges[drag.index].end = b
        }
      }
    } else if (drag.kind === 'clap-move') {
      const r = c.clapRanges[drag.i]
      const len = r.end - r.start
      r.start = clamp(snapT(t - drag.offset), 0, axis - len)
      r.end = Number((r.start + len).toFixed(1))
    } else if (drag.kind === 'clap-l') {
      const r = c.clapRanges[drag.i]
      r.start = clamp(snapT(t), 0, r.end - MIN_CLAP)
    } else if (drag.kind === 'clap-r') {
      const r = c.clapRanges[drag.i]
      r.end = clamp(snapT(t), r.start + MIN_CLAP, axis)
    }
  }

  function up() {
    if (!drag || !app.proj) return
    // 時刻順に並べ直し、重なった手拍子区間は結合する(Go側の整形と同じ規則)
    const c = app.proj.crowd
    c.keyframes.sort((a, b) => a.t - b.t)
    c.clapRanges.sort((a, b) => a.start - b.start)
    const merged: typeof c.clapRanges = []
    for (const r of c.clapRanges) {
      const last = merged[merged.length - 1]
      if (last && r.start <= last.end) last.end = Math.max(last.end, r.end)
      else merged.push({ start: r.start, end: r.end })
    }
    if (merged.length !== c.clapRanges.length) c.clapRanges = merged
    drag = null
  }

  function removeKeyframe(i: number) {
    if (tool === 'cheer') app.proj?.crowd.keyframes.splice(i, 1)
  }

  function removeRange(i: number) {
    if (tool === 'clap') app.proj?.crowd.clapRanges.splice(i, 1)
  }

  function keyKeyframe(e: KeyboardEvent, i: number) {
    if (!app.proj) return
    const k = app.proj.crowd.keyframes[i]
    if (e.key === 'Delete' || e.key === 'Backspace') {
      app.proj.crowd.keyframes.splice(i, 1)
    } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      k.cheer = snapV(k.cheer + (e.key === 'ArrowUp' ? V_SNAP : -V_SNAP))
    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      k.t = snapT(k.t + (e.key === 'ArrowRight' ? 1 : -1) * (e.shiftKey ? 1 : T_SNAP))
      app.proj.crowd.keyframes.sort((a, b) => a.t - b.t)
    } else return
    e.preventDefault()
  }

  /** 曲の始まりの歓声と、曲の終わり後の拍手・歓声を置く。 */
  function autoPlace() {
    if (!app.proj || !enabled) return
    const d = app.duration
    app.proj.crowd.keyframes = [
      { t: 0, cheer: 0.9 },
      { t: Math.min(6, d / 4), cheer: 0.15 },
      { t: Math.max(d - 4, d / 2), cheer: 0.15 },
      { t: d, cheer: 0.9 },
      { t: d + tail * 0.6, cheer: 0.6 },
      { t: axis, cheer: 0 },
    ]
  }

  function clear() {
    if (!app.proj) return
    app.proj.crowd.keyframes = []
    app.proj.crowd.clapRanges = []
  }

  const pct = (t: number) => (t / axis) * 100
  const playhead = $derived(app.region.start + player.position)
</script>

<div class="timeline">
  <div class="bar">
    <span class="title">客席タイムライン</span>
    <div class="tools" role="group" aria-label="編集ツール">
      <button class:active={tool === 'cheer'} onclick={() => (tool = 'cheer')}>歓声の強さ</button>
      <button class:active={tool === 'clap'} onclick={() => (tool = 'clap')}>手拍子区間</button>
    </div>
    <button onclick={autoPlace} disabled={!enabled} title="曲の始まりの歓声と、曲の終わり後の拍手・歓声を置く">曲前後の歓声を配置</button>
    <button class="danger" onclick={clear} disabled={keyframes.length === 0 && ranges.length === 0}>クリア</button>
    <span class="hint">
      {#if tool === 'cheer'}空いた所をクリックでキーフレーム追加 · ダブルクリックで削除{:else}空いた所をドラッグで手拍子区間 · ダブルクリックで削除{/if}
    </span>
  </div>

  <div class="canvas" bind:this={wrap} onpointerdown={down} onpointermove={move} onpointerup={up} onpointercancel={up} role="application" aria-label="客席タイムライン" class:disabled={!enabled}>
    <svg {width} height={H}>
      <!-- 0.5 と 1.0 の目安線 -->
      {#each [0, 0.5, 1] as v (v)}
        <line x1="0" x2={width} y1={yOf(v)} y2={yOf(v)} class="grid" />
      {/each}
      <!-- 曲の終わり以降(残響の尾の間) -->
      {#if enabled}
        <rect x={xOf(app.duration)} y="0" width={Math.max(0, width - xOf(app.duration))} height={H} class="after" />
      {/if}

      {#if keyframes.length}
        <polygon points={`${xOf(0)},${yOf(0)} ${curve} ${xOf(axis)},${yOf(0)}`} class="area" />
        <polyline points={curve} class="curve" />
      {/if}

      <line x1="0" x2={width} y1={LANE_Y - 2} y2={LANE_Y - 2} class="sep" />
      {#each ranges as r, i (i)}
        <g class="clap" class:editable={tool === 'clap'}>
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <rect x={xOf(r.start)} y={LANE_Y} width={Math.max(2, xOf(r.end) - xOf(r.start))} height={LANE_H} rx="3" onpointerdown={(e) => downRange(e, i, 'move')} ondblclick={() => removeRange(i)} />
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <rect class="edge" x={xOf(r.start) - EDGE_PX / 2} y={LANE_Y} width={EDGE_PX} height={LANE_H} onpointerdown={(e) => downRange(e, i, 'l')} />
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <rect class="edge" x={xOf(r.end) - EDGE_PX / 2} y={LANE_Y} width={EDGE_PX} height={LANE_H} onpointerdown={(e) => downRange(e, i, 'r')} />
        </g>
      {/each}

      {#each keyframes as k, i (i)}
        <circle
          cx={xOf(k.t)}
          cy={yOf(k.cheer)}
          r="5"
          class="kf"
          class:editable={tool === 'cheer'}
          role="slider"
          tabindex="0"
          aria-label={`歓声キーフレーム ${k.t.toFixed(1)}秒`}
          aria-valuenow={k.cheer}
          aria-valuemin="0"
          aria-valuemax="1"
          onpointerdown={(e) => downKeyframe(e, i)}
          ondblclick={() => removeKeyframe(i)}
          onkeydown={(e) => keyKeyframe(e, i)}
        >
          <title>{k.t.toFixed(1)} 秒 · 強さ {k.cheer.toFixed(2)}</title>
        </circle>
      {/each}

      {#if enabled}
        <rect x={xOf(app.region.start)} y="0" width={xOf(Math.min(app.region.len, axis))} height={H} class="region" />
        <line x1={xOf(playhead)} x2={xOf(playhead)} y1="0" y2={H} class="playhead" />
      {/if}
    </svg>
    {#if !enabled}<div class="empty">音源を追加すると編集できます</div>{/if}
  </div>
</div>

<style>
  .timeline { display: grid; gap: 6px; }
  .bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .title { color: var(--muted); font-weight: 600; margin-right: 4px; }
  .tools { display: flex; }
  .tools button { border-radius: 0; }
  .tools button:first-child { border-radius: 5px 0 0 5px; }
  .tools button:last-child { border-radius: 0 5px 5px 0; margin-left: -1px; }
  .tools button.active { background: var(--accent-dim); border-color: var(--accent); }
  .hint { color: var(--muted); font-size: 11px; margin-left: auto; }
  .canvas { position: relative; height: 78px; background: var(--panel); border: 1px solid var(--line); border-radius: 6px; overflow: hidden; touch-action: none; cursor: crosshair; }
  .canvas.disabled { opacity: 0.5; cursor: default; }
  svg { display: block; }
  .grid { stroke: var(--line); stroke-width: 1; stroke-dasharray: 2 4; }
  .sep { stroke: var(--line); }
  .after { fill: #ffffff08; }
  .area { fill: #55c58a22; }
  .curve { fill: none; stroke: var(--ok); stroke-width: 2; pointer-events: none; }
  .kf { fill: var(--bg); stroke: var(--ok); stroke-width: 2; }
  .kf.editable { cursor: grab; }
  .kf:focus-visible { stroke: var(--text); outline: none; }
  .clap rect:not(.edge) { fill: #f0b24a55; stroke: var(--warn); stroke-width: 1; }
  .clap .edge { fill: transparent; cursor: ew-resize; pointer-events: none; }
  .clap.editable rect:not(.edge) { cursor: grab; }
  .clap.editable .edge { pointer-events: all; }
  .region { fill: #5b9dff14; stroke: var(--accent); stroke-width: 1; pointer-events: none; }
  .playhead { stroke: var(--warn); stroke-width: 2; pointer-events: none; }
  .empty { position: absolute; inset: 0; display: grid; place-items: center; color: var(--muted); pointer-events: none; }
</style>
