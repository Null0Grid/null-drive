<script lang="ts">
  import qrcode from 'qrcode-generator';

  // Renders a QR code as an inline SVG path (no canvas, no data: URLs, so
  // it works under the strict CSP). Always dark-on-light with a quiet
  // zone: scanners need the contrast regardless of the page theme.

  interface Props {
    text: string;
    label: string;
    size?: number;
  }
  let { text, label, size = 176 }: Props = $props();

  const qr = $derived.by(() => {
    const q = qrcode(0, 'M');
    q.addData(text);
    q.make();
    const n = q.getModuleCount();
    let d = '';
    for (let r = 0; r < n; r++) {
      for (let c = 0; c < n; c++) {
        if (q.isDark(r, c)) d += `M${c} ${r}h1v1h-1z`;
      }
    }
    return { n, d };
  });
</script>

<svg
  class="qr"
  role="img"
  aria-label={label}
  width={size}
  height={size}
  viewBox="-4 -4 {qr.n + 8} {qr.n + 8}"
  shape-rendering="crispEdges"
>
  <rect x="-4" y="-4" width={qr.n + 8} height={qr.n + 8} fill="#fff" />
  <path d={qr.d} fill="#0a0a0f" />
</svg>

<style>
  .qr {
    display: block;
    border-radius: var(--radius-md);
  }
</style>
