<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { app } from './lib/store.svelte'
  import SourcesPanel from './lib/SourcesPanel.svelte'
  import VenuePanel from './lib/VenuePanel.svelte'
  import VenueMap from './lib/VenueMap.svelte'
  import ParamPanel from './lib/ParamPanel.svelte'
  import Waveform from './lib/Waveform.svelte'
  import Transport from './lib/Transport.svelte'
  import ExportDialog from './lib/ExportDialog.svelte'

  let showExport = $state(false)

  onMount(() => {
    app.init().catch((e) => app.fail(e))
  })

  // Projectか区間が変わったら、200ms待ってプレビューを作り直す(連続した変更は1回にまとまる)
  $effect(() => {
    if (!app.proj) return
    JSON.stringify(app.proj)
    void [app.region.start, app.region.len]
    untrack(() => app.schedulePreview())
  })

  // 原音(A/B用)は、素材・ゲイン・区間が変わったときだけ作り直す
  $effect(() => {
    if (!app.proj) return
    const key = JSON.stringify(app.proj.sources.map((s) => [s.path, s.gainDb])) + `|${app.region.start}|${app.region.len}`
    untrack(() => key && app.scheduleOriginal())
  })
</script>

<div class="app">
  <header>
    <strong class="brand">livebin</strong>
    <button onclick={() => app.newProject()}>新規</button>
    <button onclick={() => app.openProject()}>開く…</button>
    <button onclick={() => app.saveProject()}>保存</button>
    <button onclick={() => app.saveProject(true)}>名前を付けて保存…</button>
    <span class="path" title={app.projectPath}>{app.projectPath || '(未保存)'}</span>
    <button class="primary" onclick={() => (showExport = true)}>書き出し…</button>
  </header>

  <main>
    <aside class="left">
      <SourcesPanel />
      <VenuePanel />
    </aside>
    <div class="center"><VenueMap /></div>
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
  .center { overflow: hidden; }
  footer { border-top: 1px solid var(--line); padding: 10px 12px 12px; display: grid; gap: 10px; background: var(--panel); }
  .toast { position: fixed; bottom: 150px; left: 50%; transform: translateX(-50%); background: var(--panel-2); border: 1px solid var(--warn); border-radius: 6px; padding: 8px 14px; max-width: 70vw; z-index: 20; }
</style>
