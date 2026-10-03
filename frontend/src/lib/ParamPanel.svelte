<script lang="ts">
  import type { params } from '../../wailsjs/go/models'
  import { app } from './store.svelte'
  import { getPath, setPath } from './paths'
  import { clamp, decimals, fmt, fromPos, snap, toPos } from './format'

  // 音作りパネル。ListParams() の定義から自動生成する。パラメーターを足してもここは変えない。
  const GROUPS: Record<string, string> = {
    pa: 'PA質感',
    sub: 'サブウーファー',
    spatial: '空間',
    reverb: '残響',
    master: 'マスター',
  }

  // 選択肢の内部の値(JSONの値)と表示名が違うもの
  const OPTION_LABELS: Record<string, string> = { off: 'オフ', on: 'オン' }

  let showAdvanced = $state(false)
  let collapsed = $state<Record<string, boolean>>({})
  let presetName = $state('')
  let selectedPreset = $state('')

  const groups = $derived(
    Object.keys(GROUPS)
      .map((g) => ({
        id: g,
        specs: app.specs.filter((s) => s.group === g && (showAdvanced || !s.advanced)),
        all: app.specs.filter((s) => s.group === g),
      }))
      .filter((g) => g.all.length > 0),
  )

  /** 既定値。残響は会場ごとに既定値が違うので、選択中の会場プリセットの値を使う。 */
  function defaultOf(spec: params.ParamSpec): number | string {
    if (spec.path.startsWith('reverb.')) {
      const pr = app.venues.find((v) => v.id === app.proj?.venue.preset)
      const v = pr ? getPath(pr, spec.path) : undefined
      if (typeof v === 'number') return v
    }
    return spec.kind === 'enum' ? spec.options[spec.default] : spec.default
  }

  function valueOf(spec: params.ParamSpec): number | string {
    return getPath(app.proj, spec.path)
  }

  function changed(spec: params.ParamSpec): boolean {
    const v = valueOf(spec)
    const d = defaultOf(spec)
    return typeof v === 'number' && typeof d === 'number' ? Math.abs(v - d) > spec.step / 2 : v !== d
  }

  function set(spec: params.ParamSpec, v: number | string) {
    setPath(app.proj, spec.path, v)
  }

  function reset(spec: params.ParamSpec) {
    set(spec, defaultOf(spec))
  }

  function resetGroup(specs: params.ParamSpec[]) {
    for (const s of specs) reset(s)
  }

  function onNumber(spec: params.ParamSpec, text: string) {
    const x = parseFloat(text)
    if (Number.isFinite(x)) set(spec, snap(clamp(x, spec.min, spec.max), spec))
  }

  function reroll(spec: params.ParamSpec) {
    set(spec, Math.floor(Math.random() * (spec.max + 1)))
  }

  const selectedInfo = $derived(app.presets.find((p) => p.name === selectedPreset))

  async function choosePreset(name: string) {
    selectedPreset = name
    if (name) await app.applyPreset(name)
  }

  async function savePreset() {
    const name = presetName.trim()
    if (!name) return
    await app.savePreset(name)
    selectedPreset = name
    presetName = ''
  }

  async function deletePreset() {
    if (!selectedInfo || selectedInfo.readOnly) return
    await app.deletePreset(selectedInfo.name)
    selectedPreset = ''
  }
</script>

<section class="panel">
  <div class="head">
    <h2>音作り</h2>
    <label class="adv"><input type="checkbox" bind:checked={showAdvanced} /> 詳細</label>
  </div>

  <div class="presets">
    <select value={selectedPreset} onchange={(e) => choosePreset(e.currentTarget.value)} aria-label="音作りプリセット">
      <option value="">プリセットを選択…</option>
      {#each app.presets as p (p.name)}
        <option value={p.name}>{p.readOnly ? '★ ' : ''}{p.name}</option>
      {/each}
    </select>
    <button class="danger icon" disabled={!selectedInfo || selectedInfo.readOnly} onclick={deletePreset} title="選択中のユーザープリセットを削除">削除</button>
    <input type="text" placeholder="名前を付けて保存" bind:value={presetName} onkeydown={(e) => e.key === 'Enter' && savePreset()} />
    <button disabled={!presetName.trim()} onclick={savePreset}>保存</button>
  </div>

  {#if app.proj}
    <div class="groups">
      {#each groups as g (g.id)}
        <div class="group">
          <div class="group-head">
            <button class="fold" onclick={() => (collapsed[g.id] = !collapsed[g.id])} aria-expanded={!collapsed[g.id]}>
              {collapsed[g.id] ? '▸' : '▾'} {GROUPS[g.id]}
            </button>
            <button class="icon reset" onclick={() => resetGroup(g.all)} title="このグループを既定値に戻す">↺</button>
          </div>
          {#if !collapsed[g.id]}
            {#each g.specs as spec (spec.path)}
              {@const v = valueOf(spec)}
              <div class="row" class:changed={changed(spec)}>
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <span class="label" ondblclick={() => reset(spec)} title={`${spec.label}(ダブルクリックで既定値に戻す)`}>
                  <i class="dot"></i>{spec.label}
                </span>
                {#if spec.kind === 'enum'}
                  <select value={v as string} onchange={(e) => set(spec, e.currentTarget.value)}>
                    {#each spec.options as o (o)}<option value={o}>{OPTION_LABELS[o] ?? o}</option>{/each}
                  </select>
                {:else if spec.kind === 'int'}
                  <input type="number" min={spec.min} max={spec.max} step={spec.step} value={v as number} onchange={(e) => onNumber(spec, e.currentTarget.value)} />
                  <button onclick={() => reroll(spec)} title="乱数シードを変えて配置を引き直す">配置を引き直す</button>
                {:else}
                  <input
                    type="range"
                    min="0"
                    max="1"
                    step="0.001"
                    value={toPos(v as number, spec)}
                    oninput={(e) => set(spec, fromPos(parseFloat(e.currentTarget.value), spec))}
                    aria-label={spec.label}
                  />
                  <input
                    type="number"
                    min={spec.min}
                    max={spec.max}
                    step={spec.step}
                    value={fmt(v as number, spec)}
                    onchange={(e) => onNumber(spec, e.currentTarget.value)}
                    style={`width:${Math.max(6, decimals(spec.step) + 5)}ch`}
                  />
                  <span class="unit">{spec.unit}</span>
                {/if}
              </div>
            {/each}
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</section>

<style>
  .panel { display: flex; flex-direction: column; min-height: 0; height: 100%; }
  .head { display: flex; align-items: center; justify-content: space-between; }
  .adv { color: var(--muted); display: flex; gap: 4px; align-items: center; }
  .presets { display: grid; grid-template-columns: 1fr auto; gap: 6px; margin-bottom: 10px; }
  .presets input[type='text'] { text-align: left; }
  .groups { overflow-y: auto; min-height: 0; flex: 1; padding-right: 4px; }
  .group { border-top: 1px solid var(--line); padding: 6px 0 8px; }
  .group-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 4px; }
  .fold { background: none; border: none; padding: 2px 0; font-weight: 600; }
  .reset { background: none; border-color: transparent; color: var(--muted); }
  .row { display: grid; grid-template-columns: 9.5em 1fr auto 3em; align-items: center; gap: 6px; padding: 2px 0; }
  .row > select { grid-column: 2 / 5; }
  /* 長い名前は2行までに折り返す(はみ出す分は省略し、全文はツールチップで読める) */
  .label { color: var(--muted); position: relative; padding-left: 10px; cursor: default; line-height: 1.25; overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; word-break: break-all; }
  .changed .label { color: var(--text); }
  .dot { position: absolute; left: 0; top: 50%; width: 5px; height: 5px; margin-top: -2.5px; border-radius: 50%; background: transparent; }
  .changed .dot { background: var(--warn); }
  .unit { color: var(--muted); font-size: 11px; }
  .row input[type='number'] { padding: 2px 4px; appearance: textfield; }
  .row input[type='number']::-webkit-inner-spin-button { display: none; }
</style>
