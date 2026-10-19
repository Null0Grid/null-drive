<script lang="ts">
  import type { Snippet } from 'svelte';
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';

  interface Props {
    open: boolean;
    title: string;
    description?: string;
    size?: 'sm' | 'md' | 'lg';
    /** Called on Esc, backdrop click or the close button. */
    onclose: () => void;
    /** Disable closing while something is in flight. */
    dismissible?: boolean;
    children?: Snippet;
    footer?: Snippet;
  }
  let { open, title, description, size = 'sm', onclose, dismissible = true, children, footer }: Props = $props();

  const uid = $props.id();
  let panel = $state<HTMLDivElement>();
  let returnFocus: Element | null = null;

  const focusable = 'a[href], button:not([disabled]), input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])';

  // Focus management: remember what was focused, move focus into the
  // dialog (preferring [data-autofocus]), and restore it on close.
  $effect(() => {
    if (!open) return;
    returnFocus = document.activeElement;
    tick().then(() => {
      // Prefer an explicit [data-autofocus], then the first control that
      // is not the close button, then the panel itself.
      const first = [...(panel?.querySelectorAll<HTMLElement>(focusable) ?? [])].find((el) => !el.classList.contains('close'));
      const target = panel?.querySelector<HTMLElement>('[data-autofocus]') ?? first ?? panel;
      target?.focus();
    });
    return () => {
      if (returnFocus instanceof HTMLElement) returnFocus.focus();
    };
  });

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && dismissible) {
      e.stopPropagation();
      onclose();
      return;
    }
    // Keep Tab inside the dialog.
    if (e.key === 'Tab' && panel) {
      const items = [...panel.querySelectorAll<HTMLElement>(focusable)];
      if (items.length === 0) return;
      const first = items[0]!;
      const last = items[items.length - 1]!;
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }
</script>

{#if open}
  <div class="backdrop" role="presentation" onclick={(e) => dismissible && e.target === e.currentTarget && onclose()}>
    <div
      bind:this={panel}
      class="panel {size}"
      role="dialog"
      aria-modal="true"
      aria-labelledby="dlg-{uid}-title"
      aria-describedby={description ? `dlg-${uid}-desc` : undefined}
      tabindex="-1"
      {onkeydown}
    >
      <header>
        <h2 id="dlg-{uid}-title">{title}</h2>
        {#if dismissible}
          <button class="close" type="button" aria-label="Close" onclick={onclose}><Icon name="x" /></button>
        {/if}
      </header>
      {#if description}<p class="desc" id="dlg-{uid}-desc">{description}</p>{/if}
      <div class="body">{@render children?.()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: var(--z-overlay);
    display: grid;
    place-items: center;
    padding: var(--space-4);
    background: var(--backdrop);
    backdrop-filter: blur(3px);
    animation: fade var(--dur) var(--ease);
  }
  .panel {
    width: 100%;
    max-height: calc(100dvh - 2rem);
    overflow: auto;
    background: var(--panel);
    border: 1px solid var(--border-strong);
    border-top: 2px solid var(--primary);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-overlay);
    padding: var(--space-5);
    animation: rise var(--dur) var(--ease);
  }
  .panel:focus {
    outline: none;
  }
  .sm {
    max-width: 26rem;
  }
  .md {
    max-width: 34rem;
  }
  .lg {
    max-width: 46rem;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
  }
  /* Dialog titles often contain file names, and Silkscreen only has
     capitals, so they use the body font to show names exactly. */
  h2 {
    font-family: var(--font-body);
    font-weight: 700;
    font-size: var(--text-lg);
    letter-spacing: 0;
    overflow-wrap: anywhere;
  }
  .close {
    display: grid;
    place-items: center;
    width: 2rem;
    height: 2rem;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-muted);
    cursor: pointer;
  }
  .close:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .desc {
    margin-top: var(--space-2);
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .body {
    margin-top: var(--space-4);
  }
  footer {
    display: flex;
    justify-content: flex-end;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-5);
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(8px) scale(0.98);
    }
  }
  @media (max-width: 480px) {
    .backdrop {
      place-items: end stretch;
      padding: 0;
    }
    .panel {
      max-width: none;
      border-radius: var(--radius-lg) var(--radius-lg) 0 0;
    }
    footer :global(.btn) {
      flex: 1;
    }
  }
</style>
