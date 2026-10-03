<script lang="ts">
  import { onMount } from 'svelte'
  import { player } from './player.svelte'
  import { BAND_CENTERS, DB_FLOOR, Meter } from './spectrum'

  // スペクトラム表示(GEQ型)。いま出力されている音(音量つまみの後)を、1/3オクターブ31帯域のバーで表す。
  // レベルは 0 dBFS の正弦波 = 0 dB。ピークは一定時間保持する。表示中だけ描画する。
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

  const meter = new Meter(BAND_CENTERS.length)
  const bands = new Float32Array(BAND_CENTERS.length)
  const silence = new Float32Array(BAND_CENTERS.length).fill(DB_FLOOR)

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
      draw()
      raf = requestAnimationFrame(frame)
    }
    raf = requestAnimationFrame(frame)
    return () => cancelAnimationFrame(raf)
  })

  const css = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()

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

    // バー(下から 緑 → 黄(-18 dB) → 赤(-6 dB))
    const grad = g.createLinearGradient(0, yOf(DB_MIN), 0, yOf(DB_MAX))
    grad.addColorStop(0, css('--ok'))
    grad.addColorStop((-18 - DB_MIN) / (DB_MAX - DB_MIN), css('--ok'))
    grad.addColorStop((-12 - DB_MIN) / (DB_MAX - DB_MIN), css('--warn'))
    grad.addColorStop((-4 - DB_MIN) / (DB_MAX - DB_MIN), css('--warn'))
    grad.addColorStop(1, css('--danger'))
    const n = BAND_CENTERS.length
    const slot = w / n
    const barW = Math.max(2, slot - 3)
    g.textAlign = 'center'
    g.textBaseline = 'top'
    BAND_CENTERS.forEach((fc, i) => {
      const x = x0 + i * slot + (slot - barW) / 2
      const top = yOf(meter.level[i])
      g.fillStyle = grad
      g.fillRect(x, top, barW, yOf(DB_MIN) - top)
      if (meter.peak[i] > DB_MIN) {
        g.fillStyle = css('--text')
        g.fillRect(x, Math.round(yOf(meter.peak[i])) - 1, barW, 2)
      }
      const label = LABELED.get(fc)
      if (label) {
        g.fillStyle = css('--muted')
        g.fillText(label, x + barW / 2, y0 + h + 6)
      }
    })
  }
</script>

<section>
  <h2>
    スペクトラム<span class="sub"> 出力の1/3オクターブ(31帯域) · {player.mode === 'processed' ? '加工後' : '原音'}</span>
  </h2>
  <div class="box" bind:this={wrap}>
    <canvas bind:this={canvas} style={`width:${width}px;height:${height}px`} aria-label="出力のスペクトラム"></canvas>
    {#if hint}<div class="hint">{hint}</div>{/if}
  </div>
  <div class="note">縦軸は dB(0 dBFS の正弦波 = 0 dB)。バーは再生している音の帯域ごとの大きさ、白い線はピーク。</div>
</section>

<style>
  section { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .sub { font-weight: 400; margin-left: 8px; letter-spacing: 0; }
  .box { flex: 1; min-height: 0; position: relative; }
  canvas { position: absolute; inset: 0; display: block; }
  .hint { position: absolute; inset: 0; display: grid; place-items: center; color: var(--muted); pointer-events: none; text-align: center; padding: 0 20px; }
  .note { color: var(--muted); font-size: 11px; margin-top: 6px; }
</style>
