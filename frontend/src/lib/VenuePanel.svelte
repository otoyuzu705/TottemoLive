<script lang="ts">
  import { app } from './store.svelte'

  const preset = $derived(app.venues.find((v) => v.id === app.proj?.venue.preset))
</script>

<section>
  <h2>会場</h2>
  {#if app.proj}
    <select value={app.proj.venue.preset} onchange={(e) => app.applyVenue(e.currentTarget.value)} aria-label="会場プリセット">
      {#each app.venues as v (v.id)}<option value={v.id}>{v.name}</option>{/each}
    </select>
    {#if preset}
      <div class="meta">横幅 {preset.widthM} m · 奥行き {preset.depthM} m · 残響時間 {preset.rt60Sec} 秒</div>
    {/if}

    <h2 class="seat">座席(リスナー位置)</h2>
    <div class="seat-grid">
      <label>横 x<input type="number" step="0.5" bind:value={app.proj.listener.x} /></label>
      <label>奥 y<input type="number" step="0.5" bind:value={app.proj.listener.y} /></label>
      <label>高さ z<input type="number" step="0.1" bind:value={app.proj.listener.z} /></label>
      <label>向き°<input type="number" step="5" bind:value={app.proj.listener.yawDeg} /></label>
    </div>
    <div class="meta">ステージ中央が原点(m)。x は右、y は客席側。会場マップでのドラッグ操作は次のマイルストーンで追加します。</div>
  {/if}
</section>

<style>
  select { width: 100%; }
  .meta { color: var(--muted); font-size: 11px; margin-top: 6px; line-height: 1.6; }
  .seat { margin-top: 14px; }
  .seat-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; }
  .seat-grid label { display: flex; align-items: center; justify-content: space-between; gap: 6px; color: var(--muted); }
  .seat-grid input { width: 7ch; }
</style>
