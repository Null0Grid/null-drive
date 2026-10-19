<script lang="ts">
  interface Props {
    /** 0..1. Omit (or null) for an indeterminate bar. */
    value?: number | null;
    label: string;
    tone?: 'primary' | 'ok' | 'warn' | 'danger';
    /** Visible label text above the bar. */
    showLabel?: boolean;
    detail?: string;
  }
  let { value = null, label, tone = 'primary', showLabel = false, detail }: Props = $props();
  const pct = $derived(value === null || value === undefined ? null : Math.round(Math.min(1, Math.max(0, value)) * 100));
</script>

<div class="progress {tone}">
  {#if showLabel}
    <div class="meta">
      <span class="label">{label}</span>
      <span class="detail">{detail ?? (pct === null ? '' : `${pct}%`)}</span>
    </div>
  {/if}
  <div
    class="track"
    role="progressbar"
    aria-label={label}
    aria-valuemin={0}
    aria-valuemax={100}
    aria-valuenow={pct ?? undefined}
    aria-valuetext={pct === null ? 'In progress' : `${pct}%`}
  >
    <div class="fill" class:indeterminate={pct === null} style:width={pct === null ? undefined : `${pct}%`}></div>
  </div>
</div>

<style>
  .progress {
    --tone: var(--primary);
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    min-width: 0;
  }
  .ok {
    --tone: var(--ok);
  }
  .warn {
    --tone: var(--warn);
  }
  .danger {
    --tone: var(--danger);
  }
  .meta {
    display: flex;
    justify-content: space-between;
    gap: var(--space-2);
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .detail {
    flex: none;
    font-variant-numeric: tabular-nums;
  }
  .track {
    position: relative;
    height: 6px;
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: 999px;
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--tone);
    border-radius: inherit;
    box-shadow: 0 0 10px var(--tone);
    transition: width var(--dur) var(--ease);
  }
  .indeterminate {
    position: absolute;
    width: 35%;
    animation: slide 1.1s var(--ease) infinite;
  }
  @keyframes slide {
    from {
      left: -35%;
    }
    to {
      left: 100%;
    }
  }
</style>
