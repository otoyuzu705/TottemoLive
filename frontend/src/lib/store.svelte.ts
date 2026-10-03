import { untrack } from 'svelte'
import * as Go from '../../wailsjs/go/main/App'
import { EventsOn, OnFileDrop } from '../../wailsjs/runtime/runtime'
import type { main, params, project, venue } from '../../wailsjs/go/models'
import { player } from './player.svelte'
import { parseBandSeries, type BandSeries } from './spectrum'

const PREVIEW_DEBOUNCE_MS = 200

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/** アプリ全体の状態。Project はここで持ち、変更から200ms待って区間プレビューを作り直す。 */
class AppState {
  proj = $state<project.Project | null>(null)
  specs = $state<params.ParamSpec[]>([])
  venues = $state<venue.Preset[]>([])
  presets = $state<project.SoundPresetInfo[]>([])
  infos = $state<Record<string, main.SourceInfo>>({})
  peaks = $state<Record<string, number[]>>({})
  projectPath = $state('')
  /** Demucsが使えるか(任意機能) */
  stemAvailable = $state(false)
  /** ステム分離中の音源ID → ジョブIDと進捗 */
  separating = $state<Record<string, { jobId: string; ratio: number }>>({})
  /** 進行中のプレビュー作成の数 */
  busy = $state(0)
  toast = $state('')

  private timer = 0
  private origTimer = 0
  private seq = 0
  private origSeq = 0
  private toastTimer = 0

  async init() {
    const [proj, specs, venues, presets] = await Promise.all([
      Go.NewProject(),
      Go.ListParams(),
      Go.ListVenues(),
      Go.ListSoundPresets(),
    ])
    this.proj = proj
    this.specs = specs
    this.venues = venues
    this.presets = presets
    OnFileDrop((_x, _y, paths) => {
      void this.addPaths(paths)
    }, false)
    this.stemAvailable = await Go.StemSeparationAvailable()
    // separate:* イベントの中身は Go側 app.go の SeparateProgressEvent / SeparateDoneEvent / SeparateErrorEvent
    EventsOn('separate:progress', (e: { sourceId: string; ratio: number }) => {
      const cur = this.separating[e.sourceId]
      if (cur) cur.ratio = e.ratio
    })
    EventsOn('separate:done', (e: { sourceId: string; vocals: main.SourceInfo; backing: main.SourceInfo }) =>
      this.replaceWithStems(e.sourceId, e.vocals, e.backing),
    )
    EventsOn('separate:error', (e: { sourceId: string; message: string }) => {
      delete this.separating[e.sourceId]
      this.notify(`ステム分離: ${e.message}`)
    })
  }

  // --- ステム分離 ---

  async separate(sourceId: string) {
    try {
      const jobId = await Go.SeparateSource(sourceId)
      this.separating[sourceId] = { jobId, ratio: 0 }
    } catch (e) {
      this.fail(e)
    }
  }

  cancelSeparate(sourceId: string) {
    const job = this.separating[sourceId]
    if (job) void Go.CancelJob(job.jobId)
  }

  /** 分離が終わったら、元の音源を「ボーカル」「伴奏」の2本に置き換える(ゲインは引き継ぐ)。 */
  private replaceWithStems(sourceId: string, vocals: main.SourceInfo, backing: main.SourceInfo) {
    delete this.separating[sourceId]
    if (!this.proj) return
    const i = this.proj.sources.findIndex((s) => s.id === sourceId)
    if (i < 0) return
    const gainDb = this.proj.sources[i].gainDb
    for (const info of [vocals, backing]) {
      this.infos[info.id] = info
      void this.loadPeaks(info.id)
    }
    this.proj.sources.splice(
      i,
      1,
      { id: vocals.id, path: vocals.path, role: 'vocal', gainDb },
      { id: backing.id, path: backing.path, role: 'backing', gainDb },
    )
    delete this.infos[sourceId]
    delete this.peaks[sourceId]
    this.notify('ボーカルと伴奏に分離しました')
  }

  notify(msg: string) {
    this.toast = msg
    clearTimeout(this.toastTimer)
    this.toastTimer = window.setTimeout(() => (this.toast = ''), 5000)
  }

  fail(e: unknown) {
    this.notify(message(e))
  }

  /** 曲の長さ(最長の音源) */
  get duration(): number {
    if (!this.proj) return 0
    return Math.max(0, ...this.proj.sources.map((s) => this.infos[s.id]?.durationSec ?? 0))
  }

  // --- 素材 ---

  async addFiles() {
    try {
      this.attach(await Go.OpenAudioFiles())
    } catch (e) {
      this.fail(e)
    }
  }

  async addPaths(paths: string[]) {
    try {
      this.attach(await Go.AddAudioFiles(paths))
    } catch (e) {
      this.fail(e)
    }
  }

  private attach(infos: main.SourceInfo[]) {
    if (!this.proj) return
    for (const info of infos) {
      this.infos[info.id] = info
      const role = this.proj.sources.length === 0 ? 'mix' : 'backing'
      this.proj.sources.push({ id: info.id, path: info.path, role, gainDb: 0 })
      void this.loadPeaks(info.id)
    }
  }

  async loadPeaks(id: string) {
    try {
      this.peaks[id] = await Go.GetPeaks(id, 1600)
    } catch (e) {
      this.fail(e)
    }
  }

  removeSource(id: string) {
    if (!this.proj) return
    this.proj.sources = this.proj.sources.filter((s) => s.id !== id)
    delete this.infos[id]
    delete this.peaks[id]
  }

  // --- プロジェクト ---

  async newProject() {
    this.proj = await Go.NewProject()
    this.infos = {}
    this.peaks = {}
    this.projectPath = ''
  }

  async openProject() {
    try {
      const path = await Go.ChooseProjectToOpen()
      if (!path) return
      const p = await Go.LoadProject(path)
      const infos: Record<string, main.SourceInfo> = {}
      for (const s of p.sources) {
        try {
          const [info] = await Go.AddAudioFiles([s.path])
          if (info) infos[s.id] = { ...info, id: s.id }
        } catch {
          this.notify(`音源を読めません: ${s.path}`)
        }
      }
      this.proj = p
      this.infos = infos
      this.peaks = {}
      this.projectPath = path
      for (const s of p.sources) if (infos[s.id]) void this.loadPeaks(s.id)
    } catch (e) {
      this.fail(e)
    }
  }

  async saveProject(saveAs = false) {
    if (!this.proj) return
    try {
      let path = this.projectPath
      if (!path || saveAs) path = await Go.ChooseProjectSavePath()
      if (!path) return
      await Go.SaveProject(path, this.proj)
      this.projectPath = path
      this.notify('保存しました')
    } catch (e) {
      this.fail(e)
    }
  }

  // --- 会場・プリセット ---

  async applyVenue(id: string) {
    if (!this.proj) return
    try {
      this.proj = await Go.ApplyVenue(this.proj, id)
    } catch (e) {
      this.fail(e)
    }
  }

  async refreshPresets() {
    this.presets = await Go.ListSoundPresets()
  }

  async applyPreset(name: string) {
    if (!this.proj) return
    try {
      this.proj = await Go.ApplySoundPreset(this.proj, name)
    } catch (e) {
      this.fail(e)
    }
  }

  async savePreset(name: string) {
    if (!this.proj) return
    try {
      await Go.SaveSoundPreset(name, this.proj)
      await this.refreshPresets()
      this.notify(`プリセット「${name}」を保存しました`)
    } catch (e) {
      this.fail(e)
    }
  }

  async deletePreset(name: string) {
    try {
      await Go.DeleteSoundPreset(name)
      await this.refreshPresets()
    } catch (e) {
      this.fail(e)
    }
  }

  // --- プレビュー ---

  /** 変更から200ms待ってからプレビューを作り直す。連続した変更は1回にまとまる。 */
  schedulePreview() {
    clearTimeout(this.timer)
    this.timer = window.setTimeout(() => void this.renderPreview(), PREVIEW_DEBOUNCE_MS)
  }

  /** 原音(A/B用)の作り直しを予約する。素材・ゲインが変わったときだけ呼ぶ。 */
  scheduleOriginal() {
    clearTimeout(this.origTimer)
    this.origTimer = window.setTimeout(() => void this.renderOriginal(), PREVIEW_DEBOUNCE_MS)
  }

  private async renderPreview() {
    const p = untrack(() => this.proj)
    if (!p || p.sources.length === 0) return
    const mine = ++this.seq
    this.busy++
    try {
      const r = await Go.RenderPreview($state.snapshot(p) as project.Project)
      // URLが空なのは、新しい要求に追い越されて中断された印。古い結果は捨てる
      if (!r.url || mine !== this.seq) return
      // PA出力の帯域レベル(スペクトラム表示で耳に届く音に重ねる)。取れなくても再生は続ける
      const pa = await this.fetchPASeries(r)
      if (mine !== this.seq) return
      // 再生位置は保ったまま差し替える
      await player.load('processed', r.url, true)
      player.pa = pa
    } catch (e) {
      if (mine === this.seq) this.fail(e)
    } finally {
      this.busy--
    }
  }

  private async fetchPASeries(r: main.PreviewResult): Promise<BandSeries | null> {
    if (!r.bandsUrl) return null
    try {
      const res = await fetch(r.bandsUrl)
      if (!res.ok) return null
      return parseBandSeries(await res.arrayBuffer(), r)
    } catch {
      return null
    }
  }

  private async renderOriginal() {
    const p = untrack(() => this.proj)
    if (!p || p.sources.length === 0) return
    const mine = ++this.origSeq
    try {
      const url = await Go.RenderOriginal($state.snapshot(p) as project.Project)
      if (!url || mine !== this.origSeq) return
      await player.load('original', url, true)
    } catch (e) {
      if (mine === this.origSeq) this.fail(e)
    }
  }
}

export const app = new AppState()

// 開発時のみ: ブラウザのコンソールや自動テストからストアを触れるようにする(本番ビルドには入らない)
if (import.meta.env.DEV) {
  const w = window as unknown as { __app: AppState; __player: typeof player }
  w.__app = app
  w.__player = player
}
