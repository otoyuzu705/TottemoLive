// プレビュー再生。<audio> に加工後・原音の2本を切り替えて流す。
// 新しいプレビューが届いたら、再生位置とループ状態を保ったまま音源を差し替える。
export type Mode = 'processed' | 'original'

class Player {
  mode = $state<Mode>('processed')
  playing = $state(false)
  /** 区間の先頭からの再生位置(秒) */
  position = $state(0)
  /** 区間の長さ(秒)。まだ読み込んでいなければ 0 */
  duration = $state(0)
  loaded = $state<Record<Mode, boolean>>({ processed: false, original: false })

  private audio = new Audio()
  private blobs: Partial<Record<Mode, string>> = {}
  private raf = 0

  constructor() {
    this.audio.loop = true
    this.audio.addEventListener('play', () => {
      this.playing = true
      this.tick()
    })
    this.audio.addEventListener('pause', () => {
      this.playing = false
      cancelAnimationFrame(this.raf)
    })
  }

  private tick = () => {
    this.position = this.audio.currentTime
    if (this.playing) this.raf = requestAnimationFrame(this.tick)
  }

  /** 新しい音源を読み込む。keepPosition なら今の再生位置から続ける。 */
  async load(kind: Mode, url: string, keepPosition: boolean) {
    // <audio> に URL を直接渡さず、一度blobにする(WebViewのRange対応に依存せずシークできる)
    const res = await fetch(url)
    if (!res.ok) throw new Error(`プレビューを取得できません (${res.status})`)
    const blob = URL.createObjectURL(await res.blob())
    const old = this.blobs[kind]
    this.blobs[kind] = blob
    this.loaded[kind] = true
    if (this.mode === kind) await this.swap(blob, keepPosition)
    if (old) setTimeout(() => URL.revokeObjectURL(old), 5000)
  }

  private async swap(src: string, keepPosition: boolean) {
    const t = keepPosition ? this.audio.currentTime : 0
    const wasPlaying = !this.audio.paused
    this.audio.src = src
    // 読み込み完了を待つ。バックグラウンドのタブなどで読み込みが進まなくても、処理を止めないよう上限を付ける
    await new Promise<void>((resolve) => {
      const done = () => {
        clearTimeout(timer)
        resolve()
      }
      const timer = setTimeout(done, 3000)
      this.audio.addEventListener('loadedmetadata', done, { once: true })
    })
    if (Number.isFinite(this.audio.duration)) this.duration = this.audio.duration
    if (t > 0 && t < this.audio.duration) this.audio.currentTime = t
    this.position = this.audio.currentTime
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
      await this.audio.play().catch(() => {})
    } else {
      this.audio.pause()
    }
  }

  seek(sec: number) {
    if (this.audio.src) this.audio.currentTime = sec
    this.position = sec
  }

  /** 区間が変わったので、再生位置を先頭に戻す。 */
  rewind() {
    if (this.audio.src) this.audio.currentTime = 0
    this.position = 0
  }
}

export const player = new Player()
