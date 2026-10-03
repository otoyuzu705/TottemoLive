<script lang="ts">
  import { app } from './store.svelte'
  import { mmss } from './format'

  const ROLES = [
    { id: 'vocal', label: 'ボーカル' },
    { id: 'backing', label: '伴奏' },
    { id: 'mix', label: '2mix' },
  ]
</script>

<section>
  <div class="head">
    <h2>素材</h2>
    <button onclick={() => app.addFiles()}>追加…</button>
  </div>

  {#if !app.proj || app.proj.sources.length === 0}
    <div class="empty">ここに WAV / FLAC / MP3 / M4A をドラッグ&ドロップ<br />(ボーカルと伴奏の2本推奨、2mixも可)</div>
  {:else}
    {#each app.proj.sources as s (s.id)}
      {@const info = app.infos[s.id]}
      <div class="src">
        <div class="name" title={s.path}>{info?.name ?? s.path}</div>
        <div class="meta">{info ? `${mmss(info.durationSec)} · ${info.sampleRate / 1000} kHz · ${info.channels}ch` : '読み込めません'}</div>
        <select bind:value={s.role} aria-label="役割">
          {#each ROLES as r (r.id)}<option value={r.id}>{r.label}</option>{/each}
        </select>
        <button class="danger icon" onclick={() => app.removeSource(s.id)} title="削除">✕</button>
        {#if app.stemAvailable && s.role === 'mix'}
          {@const job = app.separating[s.id]}
          <div class="stem">
            {#if job}
              <progress max="1" value={job.ratio}></progress>
              <span>{Math.round(job.ratio * 100)}%</span>
              <button onclick={() => app.cancelSeparate(s.id)}>中断</button>
            {:else}
              <button onclick={() => app.separate(s.id)} title="Demucsでボーカルと伴奏に分けます(重い処理です。結果はキャッシュされます)">ボーカルと伴奏に分離</button>
            {/if}
          </div>
        {/if}
        <div class="gain">
          <span>ゲイン</span>
          <input type="range" min="-24" max="12" step="0.5" bind:value={s.gainDb} aria-label="ゲイン" />
          <input type="number" min="-24" max="12" step="0.5" bind:value={s.gainDb} />
          <span>dB</span>
        </div>
      </div>
    {/each}
  {/if}
</section>

<style>
  .head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
  .head h2 { margin: 0; }
  .empty { border: 1px dashed var(--line); border-radius: 6px; padding: 18px 10px; text-align: center; color: var(--muted); line-height: 1.7; }
  .src { display: grid; grid-template-columns: 1fr auto auto; gap: 4px 6px; padding: 8px 0; border-top: 1px solid var(--line); }
  .name { grid-column: 1 / 4; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { grid-column: 1 / 4; color: var(--muted); font-size: 11px; }
  .stem { grid-column: 1 / 4; display: flex; align-items: center; gap: 8px; }
  .stem progress { flex: 1; accent-color: var(--accent); }
  .gain { grid-column: 1 / 4; display: grid; grid-template-columns: auto 1fr 5.5ch auto; gap: 6px; align-items: center; color: var(--muted); }
  .gain input[type='number'] { padding: 2px 4px; }
</style>
