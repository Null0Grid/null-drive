<script lang="ts">
  import { passwordStrength } from '../util/strength';

  interface Props {
    password: string;
    username?: string;
  }
  let { password, username = '' }: Props = $props();
  const s = $derived(passwordStrength(password, username));
  const tones = ['var(--text-faint)', 'var(--danger)', 'var(--warn)', 'var(--primary)', 'var(--ok)'];
</script>

<div class="strength" style:--tone={tones[s.score]}>
  <div class="bars" aria-hidden="true">
    {#each [1, 2, 3, 4] as n (n)}
      <span class:on={password.length > 0 && s.score >= n}></span>
    {/each}
  </div>
  <p aria-live="polite">
    {#if s.label}<strong>{s.label}.</strong>{/if}
    {s.hint}
  </p>
</div>

<style>
  .strength {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .bars {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 4px;
  }
  .bars span {
    height: 4px;
    border-radius: 2px;
    background: var(--border);
    transition: background var(--dur) var(--ease);
  }
  .bars span.on {
    background: var(--tone);
    box-shadow: 0 0 6px var(--tone);
  }
  p {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  strong {
    color: var(--tone);
    font-weight: 700;
  }
</style>
