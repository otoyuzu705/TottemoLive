<script lang="ts">
  import * as Go from '../../wailsjs/go/main/App'
  import { EventsOn } from '../../wailsjs/runtime/runtime'
  import type { project } from '../../wailsjs/go/models'
  import { app } from './store.svelte'

  let { onclose }: { onclose: () => void } = $props()

  // render:progress / done / error の中身は Go側 app.go の ProgressEvent / DoneEvent / ErrorEvent
  type Progress = { jobId: string; stage: 'decode' | 'process' | 'encode'; ratio: number }
  const STAGES = [
    { id: 'decode', label: 'デコード' },
    { id: 'process', label: '処理' },
    { id: 'encode', label: '書き出し' },
  ] as const

  let phase = $state<'idle' | 'running' | 'done' | 'error'>('idle')
  let jobId = $state('')
  let outPath = $state('')
  let message = $state('')
  let ratios = $state<Record<string, number>>({ decode: 0, process: 0, encode: 0 })

  $effect(() => {
    const offs = [
      EventsOn('render:progress', (e: Progress) => {
        if (e.jobId === jobId) ratios[e.stage] = e.ratio
      }),
      EventsOn('render:done', (e: { jobId: string; path: string }) => {
        if (e.jobId !== jobId) return
        phase = 'done'
        outPath = e.path
      }),
      EventsOn('render:error', (e: { jobId: string; message: string }) => {
        if (e.jobId !== jobId) return
        phase = 'error'
        message = e.message
      }),
    ]
    return () => offs.forEach((off) => off())
  })

  async function start() {
    try {
      const path = await Go.ChooseExportPath()
      if (!path || !app.proj) return
      ratios = { decode: 0, process: 0, encode: 0 }
      message = ''
      phase = 'running'
      jobId = await Go.StartExport($state.snapshot(app.proj) as project.Project, path)
      outPath = path
    } catch (e) {
      phase = 'error'
      message = e instanceof Error ? e.message : String(e)
    }
  }

  function cancel() {
    if (phase === 'running') void Go.CancelJob(jobId)
  }

  function close() {
    if (phase === 'running') cancel()
    onclose()
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="backdrop" onkeydown={(e) => e.key === 'Escape' && close()}>
  <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="export-title">
    <h2 id="export-title">書き出し</h2>
    <p class="fmt">48 kHz / 24 bit ステレオ WAV(ヘッドホン再生前提)。曲全体を書き出し、音量は目標ラウドネスにそろえます。</p>

    {#if phase !== 'idle'}
      {#each STAGES as s (s.id)}
        <div class="bar">
          <span>{s.label}</span>
          <progress max="1" value={ratios[s.id]}></progress>
          <span class="pct">{Math.round(ratios[s.id] * 100)}%</span>
        </div>
      {/each}
    {/if}

    {#if phase === 'done'}<p class="ok">完了: {outPath}</p>{/if}
    {#if phase === 'error'}<p class="err" role="alert">{message}</p>{/if}

    <div class="actions">
      {#if phase === 'running'}
        <button class="danger" onclick={cancel}>中断</button>
      {:else}
        <button class="primary" onclick={start} disabled={!app.proj || app.proj.sources.length === 0}>
          {phase === 'idle' ? '書き出し先を選んで開始' : 'もう一度書き出す'}
        </button>
        <button onclick={close}>閉じる</button>
      {/if}
    </div>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: #000a; display: grid; place-items: center; z-index: 10; }
  .dialog { background: var(--panel); border: 1px solid var(--line); border-radius: 8px; padding: 18px 20px; width: min(480px, 92vw); }
  .dialog h2 { font-size: 15px; color: var(--text); }
  .fmt { color: var(--muted); line-height: 1.6; margin: 0 0 12px; }
  .bar { display: grid; grid-template-columns: 5em 1fr 3.5em; gap: 8px; align-items: center; margin: 6px 0; }
  .pct { text-align: right; color: var(--muted); font-variant-numeric: tabular-nums; }
  progress { width: 100%; accent-color: var(--accent); }
  .ok { color: var(--ok); word-break: break-all; }
  .err { color: var(--danger); }
  .actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 14px; }
</style>
