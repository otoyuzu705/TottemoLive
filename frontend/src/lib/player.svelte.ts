// プレビュー再生。<audio> に加工後・原音(曲全体)の2本を切り替えて流す。
// 新しいプレビューが届いたら、再生位置を保ったまま音源を差し替える。
// 曲全体の処理が終わる前は、シーク位置の周辺だけを先に処理した「先行プレビュー」(曲の途中から始まる短い音)を
// 再生できる。位置(position・シーク)は、音源によらず常に曲頭からの秒で扱い、音源の先頭の位置(offset)で換算する。
import { bandLevels, seriesAt, type BandSeries } from './spectrum'

export type Mode = 'processed' | 'original'

/** 音源の読み込み時の付加情報。先行プレビューは、曲の途中(offset 秒)から始まる短い音。 */
export interface LoadOptions {
  /** 音源の先頭が曲頭から何秒の位置か(既定 0 = 曲全体) */
  offset?: number
  /** 先行プレビューか(曲全体の処理がまだ終わっていない) */
  windowed?: boolean
  /** 曲全体の長さ(秒)。先行プレビューでは音源の長さと違うので、別に受け取る */
  total?: number
}

/** 先行プレビューの窓の端から、この秒数以内への移動は「窓の外」として扱う(新しい窓を要求する) */
const WINDOW_EDGE_SEC = 0.5

/** 再生音量(dB)の範囲。0 dB を超えると出力が0 dBFSを超えて歪むことがある。 */
export const VOLUME_MIN_DB = -30
export const VOLUME_MAX_DB = 6
const VOLUME_KEY = 'tottemolive.volumeDb'

/** スペクトラム表示用のFFT長。48 kHz で bin幅 約2.9 Hz、窓の長さ 約0.34 秒(低域の帯域まで分解できる) */
const ANALYSER_FFT_SIZE = 16384

function loadVolume(): number {
  try {
    const v = Number(localStorage.getItem(VOLUME_KEY))
    if (Number.isFinite(v) && localStorage.getItem(VOLUME_KEY) !== null) return Math.min(Math.max(v, VOLUME_MIN_DB), VOLUME_MAX_DB)
  } catch {
    // ストレージが使えなくても、音量は既定値(0 dB)で動く
  }
  return 0
}

class Player {
  mode = $state<Mode>('processed')
  playing = $state(false)
  /** 曲頭からの再生位置(秒) */
  position = $state(0)
  /** 再生する音源の長さ(秒、残響の尾を含む)。まだ読み込んでいなければ 0 */
  duration = $state(0)
  loaded = $state<Record<Mode, boolean>>({ processed: false, original: false })
  /** 再生音量(dB)。聞こえ方だけを変える(再計算なし)。プレビュー専用で、書き出しには反映されない */
  volumeDb = $state(loadVolume())
  muted = $state(false)
  /** 加工後のプレビューに対応する、PA出力の帯域レベル(スペクトラム表示で重ねる)。無ければ null */
  pa = $state.raw<BandSeries | null>(null)
  /** いま加工後として持っている音が先行プレビュー(曲の一部だけ)か。曲全体が届くと false になる */
  windowed = $state(false)
  /** 先行プレビューの窓の外へシークされた(曲頭からの秒)。曲全体の処理がまだ終わっていないときに呼ばれる */
  onWindowMiss: ((sec: number) => void) | null = null

  private audio = new Audio()
  private blobs: Partial<Record<Mode, string>> = {}
  /** 音源ごとの、先頭の位置(曲頭からの秒) */
  private offsets: Record<Mode, number> = { processed: 0, original: 0 }
  /** いま <audio> に入っている音源の先頭の位置(曲頭からの秒) */
  private srcOffset = 0
  /** 窓の外へシークして、新しい窓を待っている間の、続きの位置(曲頭からの秒)。無ければ null */
  private target: number | null = null
  /** 先行プレビューの終わりまで再生して止まった、または窓の外へのシークで止めた(次の音源が届いたら続きを再生する) */
  private stalled = false
  private raf = 0
  private ctx?: AudioContext
  private gain?: GainNode
  private analyser?: AnalyserNode
  private freqBuf?: Float32Array<ArrayBuffer>

  constructor() {
    this.audio.addEventListener('play', () => {
      this.playing = true
      this.tick()
    })
    this.audio.addEventListener('pause', () => {
      this.playing = false
      cancelAnimationFrame(this.raf)
    })
    this.audio.addEventListener('ended', () => {
      if (this.windowed && this.mode === 'processed') this.stalled = true
    })
  }

  private tick = () => {
    this.position = this.audio.currentTime + this.srcOffset
    if (this.playing) this.raf = requestAnimationFrame(this.tick)
  }

  /**
   * 音量用のWebAudioの経路(<audio> → GainNode → 出力)を作る。0 dB を超える増幅は
   * <audio>.volume (最大1)では出来ないため。ブラウザの規則で、ユーザー操作の中で作る必要がある。
   * 加工後・原音とも同じ <audio> を通るので、A/Bで同じ音量になる。
   * 音量の後ろにスペクトラム表示用のアナライザーを挟む(耳に届く出力そのものを見るため)。
   */
  private ensureGraph() {
    if (this.gain) return
    this.ctx = new AudioContext()
    const source = this.ctx.createMediaElementSource(this.audio)
    this.gain = this.ctx.createGain()
    this.analyser = this.ctx.createAnalyser()
    this.analyser.fftSize = ANALYSER_FFT_SIZE
    this.analyser.smoothingTimeConstant = 0 // 表示側(Meter)で動きをつけるので、ここでは平滑化しない
    this.analyser.minDecibels = -140
    this.analyser.maxDecibels = 0
    this.freqBuf = new Float32Array(this.analyser.frequencyBinCount)
    source.connect(this.gain).connect(this.analyser).connect(this.ctx.destination)
    this.applyVolume(true)
  }

  private applyVolume(immediate = false) {
    if (!this.gain || !this.ctx) return
    const g = this.muted ? 0 : Math.pow(10, this.volumeDb / 20)
    // 急に変えるとプツッというノイズが出るので、ごく短い時間で滑らかに変える
    if (immediate) this.gain.gain.value = g
    else this.gain.gain.setTargetAtTime(g, this.ctx.currentTime, 0.015)
  }

  /**
   * いま出力されている音の、1/3オクターブ帯域ごとのレベル(dB、0 dBFS の正弦波 = 0 dB)を out に書く。
   * まだ再生の経路が無い(一度も再生・音量操作をしていない)ときは false。
   */
  readBands(out: Float32Array): boolean {
    if (!this.analyser || !this.ctx || !this.freqBuf) return false
    this.analyser.getFloatFrequencyData(this.freqBuf)
    bandLevels(this.freqBuf, this.ctx.sampleRate, this.analyser.fftSize, out)
    return true
  }

  /**
   * 再生位置(曲頭からの秒)でのPA出力の帯域レベルを out に書く(全体の大きさは耳に届く出力にそろえてあり、再生音量も反映する)。
   * 加工後を聴いていないとき、または帯域レベルが無いときは false(原音にはPAが掛かっていないため)。
   */
  readPA(out: Float32Array): boolean {
    if (this.mode !== 'processed' || !this.pa) return false
    // 耳の位置のアナライザーは音量(GainNode)の後ろなので、PA出力にも同じ音量を掛ける(ミュート中は無音)
    seriesAt(this.pa, this.position, out, this.muted ? -200 : this.volumeDb)
    return true
  }

  setVolume(db: number) {
    this.volumeDb = Math.min(Math.max(db, VOLUME_MIN_DB), VOLUME_MAX_DB)
    this.muted = false
    try {
      localStorage.setItem(VOLUME_KEY, String(this.volumeDb))
    } catch {
      // 保存できなくても動作には影響しない
    }
    this.ensureGraph()
    this.applyVolume()
  }

  toggleMute() {
    this.muted = !this.muted
    this.ensureGraph()
    this.applyVolume()
  }

  /** 新しい音源を読み込む。keepPosition なら今の再生位置から続ける。 */
  async load(kind: Mode, url: string, keepPosition: boolean, opts: LoadOptions = {}) {
    // <audio> に URL を直接渡さず、一度blobにする(WebViewのRange対応に依存せずシークできる)
    const res = await fetch(url)
    if (!res.ok) throw new Error(`プレビューを取得できません (${res.status})`)
    const blob = URL.createObjectURL(await res.blob())
    const old = this.blobs[kind]
    this.blobs[kind] = blob
    this.offsets[kind] = opts.offset ?? 0
    if (kind === 'processed') this.windowed = opts.windowed ?? false
    this.loaded[kind] = true
    if (this.mode === kind) await this.swap(blob, keepPosition)
    if (opts.total && this.mode === kind) this.duration = opts.total
    if (old) setTimeout(() => URL.revokeObjectURL(old), 5000)
  }

  private async swap(src: string, keepPosition: boolean) {
    const off = this.offsets[this.mode]
    // 続きの位置(曲頭からの秒)。窓の外へのシークを待っていたときはその位置、そうでなければ今の位置
    const t = !keepPosition ? 0 : (this.target ?? this.audio.currentTime + this.srcOffset)
    this.target = null
    const wasPlaying = !this.audio.paused || this.stalled
    this.stalled = false
    this.audio.src = src
    this.srcOffset = off
    // 読み込み完了を待つ。バックグラウンドのタブなどで読み込みが進まなくても、処理を止めないよう上限を付ける
    await new Promise<void>((resolve) => {
      const done = () => {
        clearTimeout(timer)
        resolve()
      }
      const timer = setTimeout(done, 3000)
      this.audio.addEventListener('loadedmetadata', done, { once: true })
    })
    const len = this.audio.duration
    if (Number.isFinite(len)) this.duration = len + off
    // 窓(先行プレビュー)に収まらない位置なら、窓の端に寄せて止め、新しい窓を要求する
    const rel = t - off
    const outside =
      this.windowed && this.mode === 'processed' && Number.isFinite(len) && (rel < -WINDOW_EDGE_SEC || rel > len - WINDOW_EDGE_SEC)
    const at = Number.isFinite(len) ? Math.min(Math.max(rel, 0), Math.max(len - 0.1, 0)) : Math.max(rel, 0)
    if (at > 0) this.audio.currentTime = at
    this.position = this.audio.currentTime + off
    if (outside) {
      this.target = t
      this.stalled = wasPlaying
      this.audio.pause()
      this.position = t
      this.onWindowMiss?.(t)
      return
    }
    if (wasPlaying) await this.audio.play().catch(() => {})
  }

  async setMode(mode: Mode) {
    if (mode === this.mode) return
    this.mode = mode
    const src = this.blobs[mode]
    if (src) await this.swap(src, true)
  }

  async toggle() {
    if (this.audio.paused) {
      if (!this.audio.src) return
      this.ensureGraph()
      await this.ctx?.resume()
      await this.audio.play().catch(() => {})
    } else {
      this.audio.pause()
    }
  }

  seek(sec: number) {
    if (!this.audio.src) {
      this.position = sec
      return
    }
    const rel = sec - this.srcOffset
    const len = this.audio.duration
    if (this.windowed && this.mode === 'processed' && Number.isFinite(len) && (rel < 0 || rel > len - WINDOW_EDGE_SEC)) {
      // 先行プレビューの窓の外: 今の音は止めて、その位置の周辺を先に処理してもらう(届いたら続きを再生する)
      this.stalled = this.stalled || !this.audio.paused
      this.audio.pause()
      this.target = sec
      this.position = sec
      this.onWindowMiss?.(sec)
      return
    }
    this.target = null
    this.audio.currentTime = Math.max(rel, 0)
    this.position = sec
  }
}

export const player = new Player()
