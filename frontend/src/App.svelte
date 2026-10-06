<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { app } from './lib/store.svelte'
  import SourcesPanel from './lib/SourcesPanel.svelte'
  import VenuePanel from './lib/VenuePanel.svelte'
  import VenueMap from './lib/VenueMap.svelte'
  import Spectrum from './lib/Spectrum.svelte'
  import ParamPanel from './lib/ParamPanel.svelte'
  import Waveform from './lib/Waveform.svelte'
  import Transport from './lib/Transport.svelte'
  import ExportDialog from './lib/ExportDialog.svelte'
  import SettingsDialog from './lib/SettingsDialog.svelte'

  let showExport = $state(false)
  let showSettings = $state(false)

  // 中央に表示するもの: 会場マップ / スペクトラム。選んだ表示は次回の起動でも使う
  type CenterView = 'map' | 'spectrum'
  const CENTER_KEY = 'tottemolive.centerView'
  function loadCenterView(): CenterView {
    try {
      return localStorage.getItem(CENTER_KEY) === 'spectrum' ? 'spectrum' : 'map'
    } catch {
      return 'map' // ストレージが使えなくても動く
    }
  }
  let centerView = $state<CenterView>(loadCenterView())
  function chooseCenter(v: CenterView) {
    centerView = v
    try {
      localStorage.setItem(CENTER_KEY, v)
    } catch {
      // 保存できなくても動作には影響しない
    }
  }

  onMount(() => {
    app.init().catch((e) => app.fail(e))
  })

  // Projectが変わったら、200ms待ってプレビューを作り直す(連続した変更は1回にまとまる)
  $effect(() => {
    if (!app.proj) return
    JSON.stringify(app.proj)
    untrack(() => app.schedulePreview())
  })

  // 原音(A/B用)は、素材・ゲインが変わったときだけ作り直す
  $effect(() => {
    if (!app.proj) return
    const key = JSON.stringify(app.proj.sources.map((s) => [s.path, s.gainDb]))
    untrack(() => key && app.scheduleOriginal())
  })
</script>

<div class="app">
  <header>
    <strong class="brand">TottemoLive</strong>
    <button onclick={() => app.newProject()}>新規</button>
    <button onclick={() => app.openProject()}>開く…</button>
    <button onclick={() => app.saveProject()}>保存</button>
    <button onclick={() => app.saveProject(true)}>名前を付けて保存…</button>
    <span class="path" title={app.projectPath}>{app.projectPath || '(未保存)'}</span>
    <button onclick={() => (showSettings = true)}>設定…</button>
    <button class="primary" onclick={() => (showExport = true)}>書き出し…</button>
  </header>

  <main>
    <aside class="left">
      <SourcesPanel />
      <VenuePanel />
    </aside>
    <div class="center">
      <div class="tabs" role="tablist" aria-label="中央の表示">
        <button role="tab" aria-selected={centerView === 'map'} class:active={centerView === 'map'} onclick={() => chooseCenter('map')}>会場マップ</button>
        <button role="tab" aria-selected={centerView === 'spectrum'} class:active={centerView === 'spectrum'} onclick={() => chooseCenter('spectrum')}>スペクトラム</button>
      </div>
      <div class="view">
        {#if centerView === 'map'}<VenueMap />{:else}<Spectrum />{/if}
      </div>
    </div>
    <aside class="right"><ParamPanel /></aside>
  </main>

  <footer>
    <Transport />
    <Waveform />
  </footer>

  {#if app.toast}
    <div class="toast" role="status">{app.toast}</div>
  {/if}
  {#if showExport}
    <ExportDialog onclose={() => (showExport = false)} />
  {/if}
  {#if showSettings}
    <SettingsDialog onclose={() => (showSettings = false)} />
  {/if}
</div>

<style>
  .app { display: grid; grid-template-rows: auto minmax(0, 1fr) auto; height: 100%; }
  header { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-bottom: 1px solid var(--line); background: var(--panel); }
  .brand { margin-right: 10px; letter-spacing: 0.05em; }
  .path { flex: 1; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; direction: rtl; text-align: left; padding: 0 8px; }
  main { display: grid; grid-template-columns: 300px minmax(0, 1fr) 380px; grid-template-rows: minmax(0, 1fr); min-height: 0; }
  aside, .center { padding: 12px; min-height: 0; overflow-y: auto; }
  .left { border-right: 1px solid var(--line); display: flex; flex-direction: column; gap: 18px; }
  .right { border-left: 1px solid var(--line); overflow: hidden; }
  .center { overflow: hidden; display: flex; flex-direction: column; }
  .tabs { display: flex; margin-bottom: 8px; }
  .tabs button { border-radius: 0; }
  .tabs button:first-child { border-radius: 5px 0 0 5px; }
  .tabs button:last-child { border-radius: 0 5px 5px 0; margin-left: -1px; }
  .tabs button.active { background: var(--accent-dim); border-color: var(--accent); }
  .view { flex: 1; min-height: 0; }
  footer { border-top: 1px solid var(--line); padding: 10px 12px 12px; display: grid; gap: 10px; background: var(--panel); }
  .toast { position: fixed; bottom: 150px; left: 50%; transform: translateX(-50%); background: var(--panel-2); border: 1px solid var(--warn); border-radius: 6px; padding: 8px 14px; max-width: 70vw; z-index: 20; }
</style>
