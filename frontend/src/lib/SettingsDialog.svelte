<script lang="ts">
  import { onMount } from 'svelte'
  import * as Go from '../../wailsjs/go/main/App'
  import { settings } from '../../wailsjs/go/models'
  import type { main } from '../../wailsjs/go/models'
  import { formatBytes } from './format'

  let { onclose }: { onclose: () => void } = $props()

  // 編集中の値。「保存」で初めて反映する(検証に失敗したら、ここにエラーを出して設定は変えない)
  let saved = $state<settings.Settings | null>(null)
  let enabled = $state(true)
  let dir = $state('')
  let info = $state<main.CacheInfo | null>(null)
  let error = $state('')
  let busy = $state(false)
  let cleared = $state(false)

  const dirty = $derived(!!saved && (enabled !== saved.cacheEnabled || dir !== saved.cacheDir))

  async function refreshInfo() {
    try {
      info = await Go.GetCacheInfo()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  onMount(async () => {
    try {
      const s = await Go.GetSettings()
      saved = s
      enabled = s.cacheEnabled
      dir = s.cacheDir
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
    await refreshInfo()
  })

  async function pick() {
    try {
      const d = await Go.PickCacheDir()
      if (d) dir = d
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  async function clear() {
    busy = true
    error = ''
    try {
      await Go.ClearCache()
      cleared = true
      await refreshInfo()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      busy = false
    }
  }

  async function save() {
    busy = true
    error = ''
    try {
      await Go.SetSettings(settings.Settings.createFrom({ version: saved?.version ?? 1, cacheEnabled: enabled, cacheDir: dir }))
      saved = await Go.GetSettings()
      cleared = false
      await refreshInfo()
      onclose()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      busy = false
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="backdrop" onkeydown={(e) => e.key === 'Escape' && onclose()}>
  <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="settings-title">
    <h2 id="settings-title">設定</h2>

    <section>
      <h3>ディスクキャッシュ</h3>
      <p class="desc">
        プレビューの途中結果(PA・直接音・残響)を一時ファイルに置いて、音作りの調整を速くします。
        曲の長さに応じて数百MB〜数GBのディスクを使います(曲の長さ1分あたり約70MB)。
        オフにすると、調整のたびにすべて計算し直すので遅くなります。処理に必須の使い捨ての一時ファイルは、オフでも作られ、処理が終わると消えます。
      </p>

      <label class="check">
        <input type="checkbox" bind:checked={enabled} />
        キャッシュを使う
      </label>

      <div class="field">
        <span class="label">置き場所</span>
        <div class="dirrow">
          <span class="dir" class:def={!dir} title={dir || info?.dir || ''}>{dir || `既定(OSの一時フォルダ${!saved?.cacheDir && info ? ': ' + info.dir : ''})`}</span>
          <button onclick={pick} disabled={busy}>フォルダを選ぶ…</button>
          <button onclick={() => (dir = '')} disabled={busy || !dir}>既定に戻す</button>
        </div>
        <p class="hint">絶対パスのフォルダ。無ければ作り、書き込めるか確かめます。プレビューの音声と使い捨ての一時ファイルも、ここに置きます。</p>
      </div>

      <dl class="usage">
        <dt>現在の置き場所</dt>
        <dd title={info?.dir}>{info?.dir ?? '-'}</dd>
        <dt>キャッシュの使用量</dt>
        <dd>{info ? formatBytes(info.usedBytes) : '-'}{info && !info.enabled ? '(キャッシュはオフ)' : ''}</dd>
        <dt>空き容量</dt>
        <dd>{info ? (info.freeKnown ? formatBytes(info.freeBytes) : '不明') : '-'}</dd>
      </dl>
      <div class="clear">
        <button class="danger" onclick={clear} disabled={busy || !info || info.usedBytes === 0}>キャッシュを消去</button>
        {#if cleared}<span class="ok">消去しました</span>{/if}
      </div>
    </section>

    {#if error}<p class="err" role="alert">{error}</p>{/if}

    <div class="actions">
      <button class="primary" onclick={save} disabled={busy || !dirty}>保存</button>
      <button onclick={onclose}>閉じる</button>
    </div>
    {#if dirty}<p class="hint note">保存すると、実行中のプレビューは中断され、置き場所かオン・オフを変えたときは、いまのキャッシュを破棄して作り直します。</p>{/if}
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: #000a; display: grid; place-items: center; z-index: 10; }
  .dialog { background: var(--panel); border: 1px solid var(--line); border-radius: 8px; padding: 18px 20px; width: min(560px, 94vw); max-height: 92vh; overflow-y: auto; }
  .dialog h2 { font-size: 15px; color: var(--text); margin: 0 0 8px; }
  h3 { font-size: 13px; margin: 8px 0; color: var(--text); }
  .desc, .hint { color: var(--muted); line-height: 1.6; margin: 0 0 10px; }
  .hint { font-size: 12px; margin: 4px 0 0; }
  .note { margin-top: 8px; }
  .check { display: flex; align-items: center; gap: 8px; margin: 6px 0 12px; cursor: pointer; }
  .field { margin-bottom: 12px; }
  .label { display: block; margin-bottom: 4px; color: var(--muted); }
  .dirrow { display: flex; gap: 6px; align-items: center; }
  .dir { flex: 1; min-width: 0; background: var(--bg); border: 1px solid var(--line); border-radius: 4px; padding: 4px 8px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; user-select: text; }
  .dir.def { color: var(--muted); }
  .usage { display: grid; grid-template-columns: 9em 1fr; gap: 4px 10px; margin: 0 0 10px; }
  .usage dt { color: var(--muted); }
  .usage dd { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; user-select: text; font-variant-numeric: tabular-nums; }
  .clear { display: flex; align-items: center; gap: 10px; }
  .ok { color: var(--ok); }
  .err { color: var(--danger); word-break: break-all; margin: 10px 0 0; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 14px; }
</style>
