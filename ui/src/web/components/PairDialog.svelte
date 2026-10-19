<script lang="ts">
  import { untrack } from 'svelte';
  import Modal from '../../lib/components/Modal.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import QrCode from '../../lib/components/QrCode.svelte';
  import type { Device, PairingResponse } from '../../lib/api/types';
  import { client } from '../api';
  import { errorMessage } from '../actions';

  // "Add device": issues a 5-minute single-use code, shows it with a QR
  // code (server address + code), and waits for the device to claim it.

  interface Props {
    open: boolean;
    onclose: () => void;
    onpaired?: (d: Device) => void;
    /** Offer the manual token flow (Settings only). */
    onmanual?: () => void;
  }
  let { open, onclose, onpaired, onmanual }: Props = $props();

  let pairing = $state<PairingResponse | null>(null);
  let code = $state('');
  let error = $state<string | null>(null);
  let now = $state(Date.now());
  let copied = $state(false);

  const remaining = $derived(pairing ? Math.max(0, new Date(pairing.expires_at).getTime() - now) : 0);
  const mmss = $derived(`${Math.floor(remaining / 60000)}:${String(Math.floor((remaining % 60000) / 1000)).padStart(2, '0')}`);
  const link = $derived(
    code ? `nulldrive://pair?${new URLSearchParams({ code, server: window.location.origin }).toString()}` : '',
  );
  const status = $derived(pairing && pairing.status === 'pending' && remaining === 0 ? 'expired' : pairing?.status);

  async function issue() {
    error = null;
    pairing = null;
    code = '';
    copied = false;
    try {
      const p = await client.createPairing();
      pairing = p;
      code = p.code ?? '';
    } catch (e) {
      error = errorMessage(e);
    }
  }

  // Lifecycle: a fresh code each time the dialog opens; poll every 2s while
  // pending; cancel an unused code when the dialog closes.
  $effect(() => {
    if (!open) return;
    untrack(() => void issue());
    const tick = setInterval(() => (now = Date.now()), 1000);
    const poll = setInterval(async () => {
      const p = pairing;
      if (!p || p.status !== 'pending') return;
      try {
        const next = await client.getPairing(p.id);
        if (pairing?.id !== p.id) return; // a newer code replaced it meanwhile
        pairing = { ...next, code: p.code };
        if (next.status === 'claimed' && next.device) onpaired?.(next.device);
      } catch {
        /* transient: next poll retries */
      }
    }, 2000);
    return () => {
      clearInterval(tick);
      clearInterval(poll);
      const p = pairing;
      if (p && p.status === 'pending') void client.cancelPairing(p.id).catch(() => {});
      pairing = null;
      code = '';
    };
  });

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(link);
      copied = true;
    } catch {
      /* the link is visible to copy manually */
    }
  }
</script>

<Modal {open} title="Add a device" size="md" {onclose}>
  {#if error}
    <p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>
  {:else if !pairing}
    <p class="muted" aria-busy="true">Creating a code…</p>
  {:else if status === 'claimed'}
    <div class="done" role="status">
      <span class="ok-badge"><Icon name="check" size={28} /></span>
      <div>
        <strong>Paired with “{pairing.device?.name}”</strong>
        <p class="muted">It is syncing now and appears in your device list.</p>
      </div>
    </div>
  {:else if status === 'expired' || status === 'cancelled'}
    <div class="expired" role="status">
      <p><strong>This code has expired.</strong> Codes only work for 5 minutes.</p>
      <Button variant="primary" icon="refresh" onclick={issue}>New code</Button>
    </div>
  {:else}
    <div class="pair">
      <div class="steps">
        <ol>
          <li>Open the Null Drive desktop app on the new computer.</li>
          <li>Choose this server, then <em>Pair with a code</em>.</li>
          <li>Enter the code:</li>
        </ol>
        <p class="code" aria-label="Pairing code {code.split('').join(' ')}" data-testid="pairing-code">{code}</p>
        <p class="timer" aria-live="off">
          <span class="dot" aria-hidden="true"></span><span>Waiting for the device · expires in <span class="mono">{mmss}</span></span>
        </p>
      </div>
      <figure>
        <QrCode text={link} label="QR code with the server address and pairing code" />
        <figcaption>
          <button type="button" class="linkbtn" onclick={copyLink}>{copied ? 'Link copied' : 'Copy pairing link'}</button>
        </figcaption>
      </figure>
    </div>
    <p class="muted small">Single use. Anyone with this code in the next few minutes can add a device to your account, so don't share it.</p>
  {/if}
  {#snippet footer()}
    {#if onmanual && status !== 'claimed'}
      <button type="button" class="linkbtn manual" onclick={onmanual}>Create an API token instead</button>
    {/if}
    <Button variant={status === 'claimed' ? 'primary' : 'ghost'} onclick={onclose}>{status === 'claimed' ? 'Done' : 'Close'}</Button>
  {/snippet}
</Modal>

<style>
  .pair {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: var(--space-5);
    align-items: center;
  }
  .steps ol {
    margin: 0 0 var(--space-3);
    padding-left: 1.2rem;
    color: var(--text-muted);
    font-size: var(--text-sm);
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
  }
  .code {
    font-family: var(--font-display);
    font-size: clamp(1.25rem, 4.6vw, 1.75rem);
    letter-spacing: 0.06em;
    white-space: nowrap;
    color: var(--primary);
    text-shadow: var(--glow-text);
    user-select: all;
    margin: 0;
  }
  .timer {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-2);
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .mono {
    font-family: var(--font-mono);
  }
  .dot {
    flex: none;
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 50%;
    background: var(--primary);
    animation: blink 1.4s ease-in-out infinite;
  }
  @keyframes blink {
    50% {
      opacity: 0.2;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .dot {
      animation: none;
    }
  }
  figure {
    margin: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-2);
  }
  .linkbtn {
    background: none;
    border: 0;
    padding: 0;
    color: var(--primary);
    font: inherit;
    font-size: var(--text-sm);
    cursor: pointer;
    text-decoration: underline;
    text-underline-offset: 3px;
  }
  .manual {
    margin-right: auto;
    color: var(--text-muted);
  }
  .done,
  .expired {
    display: flex;
    align-items: center;
    gap: var(--space-4);
  }
  .expired {
    justify-content: space-between;
    flex-wrap: wrap;
  }
  .ok-badge {
    display: grid;
    place-items: center;
    width: 3rem;
    height: 3rem;
    border-radius: 50%;
    color: var(--ok);
    background: var(--ok-soft);
    box-shadow: 0 0 12px var(--ok-soft);
  }
  .muted {
    color: var(--text-muted);
  }
  .small {
    font-size: var(--text-sm);
    margin-top: var(--space-4);
  }
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--danger);
  }
  @media (max-width: 560px) {
    .pair {
      grid-template-columns: 1fr;
      justify-items: center;
      text-align: center;
    }
    .timer {
      justify-content: center;
    }
  }
</style>
