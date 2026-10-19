<script lang="ts">
  // Null Drive mark: the "null" glyph (a circle crossed by a double slash,
  // //) inside a drive bay, with a pink activity LED. Two variants share the
  // geometry (scripts/gen-icons.py renders the same shapes into the app
  // icons, so keep the coordinates in sync):
  //   client: cyan slashes, the desktop app
  //   server: red slashes, the server and the web app it serves
  // Without a variant prop the slash color comes from --logo-slash, which
  // each app sets once (the web app sets it to red in app.css).
  interface Props {
    size?: number;
    /** Show the "NULL DRIVE" wordmark next to the mark. */
    wordmark?: boolean;
    variant?: 'client' | 'server';
  }
  let { size = 32, wordmark = true, variant }: Props = $props();
</script>

<span class="logo" class:client={variant === 'client'} class:server={variant === 'server'} style:--logo-size="{size}px">
  <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <rect x="2.5" y="6.5" width="27" height="19" rx="3" class="bay" />
    <circle cx="16" cy="16" r="5.5" class="null" />
    <path d="M10.75 22 17.25 10M14.75 22 21.25 10" class="slash" />
    <path d="M5.5 22.5h2" class="led" />
  </svg>
  {#if wordmark}
    <span class="word"><span class="null-word">Null</span><span class="drive-word">Drive</span></span>
  {:else}
    <span class="sr-only">Null Drive</span>
  {/if}
</span>

<style>
  .logo {
    display: inline-flex;
    align-items: center;
    gap: 0.6em;
    color: var(--text);
    font-family: var(--font-display);
    font-size: calc(var(--logo-size) * 0.55);
    line-height: 1;
    white-space: nowrap;
  }
  svg {
    flex: none;
    overflow: visible;
  }
  .bay {
    fill: var(--surface);
    stroke: var(--border-strong);
    stroke-width: 1.5;
  }
  .logo {
    --slash: var(--logo-slash, var(--primary));
    --slash-glow: var(--logo-slash-glow, var(--primary-line));
  }
  .logo.client {
    --slash: var(--primary);
    --slash-glow: var(--primary-line);
  }
  .logo.server {
    --slash: var(--brand-server);
    --slash-glow: var(--brand-server-line);
  }
  .null,
  .slash {
    fill: none;
    stroke: var(--primary);
    stroke-width: 2;
    stroke-linecap: round;
    filter: drop-shadow(0 0 3px var(--primary-line));
  }
  .slash {
    stroke: var(--slash);
    filter: drop-shadow(0 0 3px var(--slash-glow));
  }
  .led {
    stroke: var(--accent);
    stroke-width: 2;
    stroke-linecap: round;
  }
  .word {
    display: inline-flex;
    gap: 0.35em;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
  .null-word {
    color: var(--primary);
    text-shadow: var(--glow-text);
  }
</style>
