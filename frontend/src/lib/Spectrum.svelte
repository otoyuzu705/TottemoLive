<script lang="ts">
  import { onMount } from 'svelte'
  import { player } from './player.svelte'
  import { BAND_CENTERS, DB_FLOOR, Meter } from './spectrum'

  // スペクトラム表示(GEQ型)。いま出力されている音(音量つまみの後)を、1/3オクターブ31帯域で表す。
  // 表示は「バー」と「折れ線」を切り替えられる。レベルは 0 dBFS の正弦波 = 0 dB。ピークは一定時間保持する。
  // 表示中だけ描画する。
  type Style = 'bars' | 'line'
  const STYLE_KEY = 'tottemolive.spectrumStyle'
  function loadStyle(): Style {
    try {
      return localStorage.getItem(STYLE_KEY) === 'line' ? 'line' : 'bars'
    } catch {
      return 'bars' // ストレージが使えなくても動く
    }
  }
  let style = $state<Style>(loadStyle())
  function chooseStyle(s: Style) {
    style = s
    try {
      localStorage.setItem(STYLE_KEY, s)
    } catch {
      // 保存できなくても動作には影響しない
    }
  }

  // PA出力(スピーカーに送る音)を重ねるか。選んだ設定は次回も使う
  const PA_KEY = 'tottemolive.spectrumPA'
  function loadShowPA(): boolean {
    try {
      return localStorage.getItem(PA_KEY) !== 'off'
    } catch {
      return true
    }
  }
  let showPA = $state(loadShowPA())
  function setShowPA(on: boolean) {
    showPA = on
    try {
      localStorage.setItem(PA_KEY, on ? 'on' : 'off')
    } catch {
      // 保存できなくても動作には影響しない
    }
  }

  const DB_MIN = -80
  const DB_MAX = 0
  const DB_LINES = [0, -10, -20, -30, -40, -50, -60, -70, -80]
  const LABELED = new Map([
    [31.5, '31.5'], [63, '63'], [125, '125'], [250, '250'], [500, '500'],
    [1000, '1k'], [2000, '2k'], [4000, '4k'], [8000, '8k'], [16000, '16k'],
  ])
  const PAD = { left: 40, right: 10, top: 10, bottom: 24 }

  let wrap: HTMLDivElement | undefined = $state()
  let canvas: HTMLCanvasElement | undefined = $state()
  let width = $state(0)
  let height = $state(0)
  // マウスが乗っている帯域(折れ線のとき、周波数とレベルを読み出す)
  let hover = -1

  const meter = new Meter(BAND_CENTERS.length)
  const bands = new Float32Array(BAND_CENTERS.length)
  const silence = new Float32Array(BAND_CENTERS.length).fill(DB_FLOOR)
  const paBands = new Float32Array(BAND_CENTERS.length)
  // いま重ねているPA出力があるか(フレームごとに更新)
  let paShown = false

  const hint = $derived(
    !player.loaded.processed ? 'プレビューができると、再生した音のスペクトラムが表示されます' : !player.playing ? '再生するとスペクトラムが表示されます' : '',
  )

  $effect(() => {
    if (!wrap) return
    const ro = new ResizeObserver(([e]) => {
      width = Math.floor(e.contentRect.width)
      height = Math.floor(e.contentRect.height)
    })
    ro.observe(wrap)
    return () => ro.disconnect()
  })

  onMount(() => {
    let raf = 0
    let last = performance.now()
    const frame = (now: number) => {
      const dt = Math.min(0.1, (now - last) / 1000)
      last = now
      // 再生していないときは、読み取りをやめて静かに落とす(止めた直後の表示が残らないように)
      const live = player.playing && player.readBands(bands)
      meter.update(live ? bands : silence, dt)
      paShown = showPA && live && player.readPA(paBands)
      draw()
      raf = requestAnimationFrame(frame)
    }
    raf = requestAnimationFrame(frame)
    return () => cancelAnimationFrame(raf)
  })

  const css = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()

  function onMove(e: PointerEvent) {
    if (!canvas) return
    const r = canvas.getBoundingClientRect()
    const slot = (width - PAD.left - PAD.right) / BAND_CENTERS.length
    const i = Math.floor((e.clientX - r.left - PAD.left) / slot)
    hover = i >= 0 && i < BAND_CENTERS.length ? i : -1
  }

  const fmtHz = (f: number) => (f >= 1000 ? `${f / 1000} kHz` : `${f} Hz`)

  /** 点列をなめらかに通る曲線(Catmull-Rom → ベジェ)を、いまのパスに足す。 */
  function smoothThrough(g: CanvasRenderingContext2D, pts: { x: number; y: number }[]) {
    g.moveTo(pts[0].x, pts[0].y)
    for (let i = 0; i < pts.length - 1; i++) {
      const p0 = pts[Math.max(0, i - 1)]
      const p1 = pts[i]
      const p2 = pts[i + 1]
      const p3 = pts[Math.min(pts.length - 1, i + 2)]
      g.bezierCurveTo(
        p1.x + (p2.x - p0.x) / 6, p1.y + (p2.y - p0.y) / 6,
        p2.x - (p3.x - p1.x) / 6, p2.y - (p3.y - p1.y) / 6,
        p2.x, p2.y,
      )
    }
  }

  function draw() {
    if (!canvas || width === 0 || height === 0) return
    const dpr = window.devicePixelRatio || 1
    if (canvas.width !== Math.round(width * dpr) || canvas.height !== Math.round(height * dpr)) {
      canvas.width = Math.round(width * dpr)
      canvas.height = Math.round(height * dpr)
    }
    const g = canvas.getContext('2d')!
    g.setTransform(dpr, 0, 0, dpr, 0, 0)
    g.clearRect(0, 0, width, height)

    const x0 = PAD.left
    const y0 = PAD.top
    const w = width - PAD.left - PAD.right
    const h = height - PAD.top - PAD.bottom
    const yOf = (db: number) => y0 + (1 - (Math.min(Math.max(db, DB_MIN), DB_MAX) - DB_MIN) / (DB_MAX - DB_MIN)) * h

    // 目盛り(dB)
    g.font = '11px sans-serif'
    g.textAlign = 'right'
    g.textBaseline = 'middle'
    g.lineWidth = 1
    for (const db of DB_LINES) {
      const y = Math.round(yOf(db)) + 0.5
      g.strokeStyle = css('--line')
      g.beginPath()
      g.moveTo(x0, y)
      g.lineTo(x0 + w, y)
      g.stroke()
      g.fillStyle = css('--muted')
      g.fillText(String(db), x0 - 6, y)
    }

    const n = BAND_CENTERS.length
    const slot = w / n
    const centerX = (i: number) => x0 + i * slot + slot / 2

    if (style === 'bars') {
      // バー(下から 緑 → 黄(-18 dB) → 赤(-6 dB))
      const grad = g.createLinearGradient(0, yOf(DB_MIN), 0, yOf(DB_MAX))
      grad.addColorStop(0, css('--ok'))
      grad.addColorStop((-18 - DB_MIN) / (DB_MAX - DB_MIN), css('--ok'))
      grad.addColorStop((-12 - DB_MIN) / (DB_MAX - DB_MIN), css('--warn'))
      grad.addColorStop((-4 - DB_MIN) / (DB_MAX - DB_MIN), css('--warn'))
      grad.addColorStop(1, css('--danger'))
      const barW = Math.max(2, slot - 3)
      BAND_CENTERS.forEach((_, i) => {
        const x = centerX(i) - barW / 2
        const top = yOf(meter.level[i])
        g.fillStyle = grad
        g.fillRect(x, top, barW, yOf(DB_MIN) - top)
        if (meter.peak[i] > DB_MIN) {
          g.fillStyle = css('--text')
          g.fillRect(x, Math.round(yOf(meter.peak[i])) - 1, barW, 2)
        }
      })
    } else {
      // 折れ線: レベルの曲線(下を塗りつぶす)と、ピークの点線
      const pts = BAND_CENTERS.map((_, i) => ({ x: centerX(i), y: yOf(meter.level[i]) }))
      const peaks = BAND_CENTERS.map((_, i) => ({ x: centerX(i), y: yOf(meter.peak[i]) }))
      g.save()
      g.beginPath()
      g.rect(x0, y0, w, h) // 曲線が補間で枠の外へはみ出さないようにする
      g.clip()

      g.beginPath()
      smoothThrough(g, pts)
      g.lineTo(pts[n - 1].x, yOf(DB_MIN))
      g.lineTo(pts[0].x, yOf(DB_MIN))
      g.closePath()
      const fill = g.createLinearGradient(0, y0, 0, y0 + h)
      fill.addColorStop(0, css('--accent') + '66')
      fill.addColorStop(1, css('--accent') + '08')
      g.fillStyle = fill
      g.fill()

      g.setLineDash([4, 3])
      g.lineWidth = 1
      g.strokeStyle = css('--muted')
      g.beginPath()
      smoothThrough(g, peaks)
      g.stroke()
      g.setLineDash([])

      g.lineWidth = 2
      g.lineJoin = 'round'
      g.strokeStyle = css('--accent')
      g.beginPath()
      smoothThrough(g, pts)
      g.stroke()
      g.restore()
    }

    // PA出力(スピーカーに送る音)。全体の大きさを耳に届く出力にそろえてあるので、帯域ごとの差が音色の違いになる
    if (paShown) {
      g.strokeStyle = css('--warn')
      g.fillStyle = css('--warn')
      g.lineWidth = 2
      if (style === 'bars') {
        const barW = Math.max(2, slot - 3)
        BAND_CENTERS.forEach((_, i) => {
          if (paBands[i] <= DB_MIN) return
          g.fillRect(centerX(i) - barW / 2 - 1, Math.round(yOf(paBands[i])) - 1, barW + 2, 3)
        })
      } else {
        g.save()
        g.beginPath()
        g.rect(x0, y0, w, h)
        g.clip()
        g.beginPath()
        smoothThrough(g, BAND_CENTERS.map((_, i) => ({ x: centerX(i), y: yOf(paBands[i]) })))
        g.stroke()
        g.restore()
      }
    }

    // 周波数ラベル
    g.textAlign = 'center'
    g.textBaseline = 'top'
    g.fillStyle = css('--muted')
    BAND_CENTERS.forEach((fc, i) => {
      const label = LABELED.get(fc)
      if (label) g.fillText(label, centerX(i), y0 + h + 6)
    })

    // 折れ線のとき、マウスの位置の帯域の周波数とレベルを読み出す
    if (style === 'line' && hover >= 0) {
      const x = centerX(hover)
      const y = yOf(meter.level[hover])
      g.strokeStyle = css('--muted')
      g.lineWidth = 1
      g.setLineDash([2, 3])
      g.beginPath()
      g.moveTo(x, y0)
      g.lineTo(x, y0 + h)
      g.stroke()
      g.setLineDash([])
      g.fillStyle = css('--accent')
      g.beginPath()
      g.arc(x, y, 4, 0, Math.PI * 2)
      g.fill()
      const db = meter.level[hover]
      const paDb = paBands[hover]
      const text =
        `${fmtHz(BAND_CENTERS[hover])}  ${db <= DB_FLOOR ? '−∞' : db.toFixed(1)} dB` +
        (paShown ? `  (PA ${paDb <= DB_FLOOR ? '−∞' : paDb.toFixed(1)})` : '')
      g.font = '12px sans-serif'
      const tw = g.measureText(text).width + 12
      const bx = Math.min(Math.max(x - tw / 2, x0), x0 + w - tw)
      g.fillStyle = css('--panel-2')
      g.strokeStyle = css('--line')
      g.beginPath()
      g.rect(bx, y0 + 4, tw, 20)
      g.fill()
      g.stroke()
      g.fillStyle = css('--text')
      g.textAlign = 'center'
      g.textBaseline = 'middle'
      g.fillText(text, bx + tw / 2, y0 + 14)
    }
  }
</script>

<section>
  <div class="head">
    <h2>
      スペクトラム<span class="sub"> 出力の1/3オクターブ(31帯域) · {player.mode === 'processed' ? '加工後' : '原音'}</span>
    </h2>
    <label class="pa" title={player.pa ? 'スピーカーに送る音(PAの出力)を重ねます。全体の大きさは耳に届く音にそろえてあるので、帯域ごとの差が、距離・空気吸収・残響などによる音色の違いになります' : '加工後のプレビューができると使えます'}>
      <input type="checkbox" checked={showPA} disabled={!player.pa} onchange={(e) => setShowPA(e.currentTarget.checked)} />
      <i class="swatch"></i>PA出力を重ねる
    </label>
    <div class="styles" role="group" aria-label="表示の種類">
      <button class:active={style === 'bars'} aria-pressed={style === 'bars'} onclick={() => chooseStyle('bars')}>バー</button>
      <button class:active={style === 'line'} aria-pressed={style === 'line'} onclick={() => chooseStyle('line')}>折れ線</button>
    </div>
  </div>
  <div class="box" bind:this={wrap}>
    <canvas
      bind:this={canvas}
      style={`width:${width}px;height:${height}px`}
      aria-label="出力のスペクトラム"
      onpointermove={onMove}
      onpointerleave={() => (hover = -1)}
    ></canvas>
    {#if hint}<div class="hint">{hint}</div>{/if}
  </div>
  <div class="note">
    縦軸は dB(0 dBFS の正弦波 = 0 dB)。{player.pa && showPA ? '黄色がPA出力(スピーカーに送る音。全体の大きさは耳に届く音にそろえて表示)。' : ''}{style === 'bars' ? 'バーは再生している音の帯域ごとの大きさ、白い線はピーク。' : '曲線は帯域ごとの大きさ、点線はピーク。マウスを乗せると周波数とレベルが読めます。'}
  </div>
</section>

<style>
  section { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .head { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; }
  .sub { font-weight: 400; margin-left: 8px; letter-spacing: 0; }
  .pa { display: flex; align-items: center; gap: 5px; margin-left: auto; color: var(--muted); }
  .swatch { width: 14px; height: 3px; background: var(--warn); display: inline-block; }
  .styles { display: flex; }
  .styles button { border-radius: 0; padding: 2px 10px; }
  .styles button:first-child { border-radius: 5px 0 0 5px; }
  .styles button:last-child { border-radius: 0 5px 5px 0; margin-left: -1px; }
  .styles button.active { background: var(--accent-dim); border-color: var(--accent); }
  .box { flex: 1; min-height: 0; position: relative; }
  canvas { position: absolute; inset: 0; display: block; }
  .hint { position: absolute; inset: 0; display: grid; place-items: center; color: var(--muted); pointer-events: none; text-align: center; padding: 0 20px; }
  .note { color: var(--muted); font-size: 11px; margin-top: 6px; }
</style>
