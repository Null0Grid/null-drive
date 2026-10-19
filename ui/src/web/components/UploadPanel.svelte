<script lang="ts">
  import Button from '../../lib/components/Button.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import ProgressBar from '../../lib/components/ProgressBar.svelte';
  import { formatBytes } from '../../lib/util/format';
  import { basename } from '../../lib/util/paths';
  import type { UploadQueue } from '../uploads.svelte';

  interface Props {
    queue: UploadQueue;
  }
  let { queue }: Props = $props();
  let collapsed = $state(false);

  const t = $derived(queue.totals);
  const title = $derived(
    t.active > 0
      ? `Uploading ${t.active} file${t.active === 1 ? '' : 's'}`
      : t.failed > 0
        ? `${t.failed} upload${t.failed === 1 ? '' : 's'} failed`
        : 'Uploads complete',
  );
</script>

{#if queue.jobs.length > 0}
  <section class="panel" aria-label="Uploads" class:failed={t.active === 0 && t.failed > 0}>
    <header>
      <div class="head">
        <strong>{title}</strong>
        <span class="bytes">{formatBytes(t.loaded)} / {formatBytes(t.total)}</span>
      </div>
      <Button
        variant="ghost"
        size="sm"
        iconOnly
        icon={collapsed ? 'chevron-down' : 'arrow-down'}
        label={collapsed ? 'Expand uploads' : 'Collapse uploads'}
        onclick={() => (collapsed = !collapsed)}
      />
      {#if t.active === 0}
        <Button variant="ghost" size="sm" iconOnly icon="x" label="Clear finished uploads" onclick={() => queue.clearFinished()} />
      {/if}
    </header>
    <ProgressBar value={t.fraction} label="Total upload progress" tone={t.active === 0 && t.failed === 0 ? 'ok' : 'primary'} />
    {#if !collapsed}
      <ul>
        {#each queue.jobs as job (job.id)}
          <li class={job.status}>
            <span class="status" aria-hidden="true">
              {#if job.status === 'done'}<Icon name="check" size={16} />{:else if job.status === 'error'}<Icon
                  name="alert"
                  size={16}
                />{:else if job.status === 'cancelled'}<Icon name="x" size={16} />{:else}<Icon name="upload" size={16} />{/if}
            </span>
            <div class="info">
              <span class="name" title={job.renamedTo ?? job.path}>{basename(job.renamedTo ?? job.path)}</span>
              {#if job.status === 'uploading'}
                <ProgressBar value={job.file.size ? job.loaded / job.file.size : null} label="Uploading {job.file.name}" />
              {:else if job.status === 'error'}
                <span class="detail err">{job.error}</span>
              {:else if job.renamedTo}
                <span class="detail warn">Changed on the server meanwhile, so it was kept as a copy</span>
              {:else}
                <span class="detail">{job.status === 'queued' ? 'Waiting' : job.status === 'cancelled' ? 'Cancelled' : formatBytes(job.file.size)}</span>
              {/if}
            </div>
            {#if job.status === 'queued' || job.status === 'uploading'}
              <Button variant="ghost" size="sm" iconOnly icon="x" label="Cancel {job.file.name}" onclick={() => queue.cancel(job.id)} />
            {:else if job.status === 'error' || job.status === 'cancelled'}
              <Button variant="ghost" size="sm" iconOnly icon="refresh" label="Retry {job.file.name}" onclick={() => queue.retry(job.id)} />
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
{/if}

<style>
  .panel {
    position: fixed;
    right: var(--space-4);
    bottom: var(--space-4);
    z-index: 40;
    width: min(24rem, calc(100vw - 2rem));
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    padding: var(--space-3) var(--space-3) var(--space-4) var(--space-4);
    background: var(--panel);
    border: 1px solid var(--primary-line);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-overlay), var(--glow-primary);
  }
  .panel.failed {
    border-color: var(--danger);
    box-shadow: var(--shadow-overlay);
  }
  header {
    display: flex;
    align-items: center;
    gap: var(--space-1);
  }
  .head {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  strong {
    font-size: var(--text-sm);
    font-weight: 500;
  }
  .bytes {
    font-family: var(--font-term);
    font-size: 1.05rem;
    color: var(--text-muted);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 14rem;
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--space-2);
  }
  .status {
    display: flex;
    color: var(--primary);
  }
  .done .status {
    color: var(--ok);
  }
  .error .status {
    color: var(--danger);
  }
  .cancelled .status {
    color: var(--text-faint);
  }
  .info {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .name {
    font-size: var(--text-sm);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .detail {
    font-size: var(--text-xs);
    color: var(--text-faint);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .detail.err {
    color: var(--danger);
    white-space: normal;
  }
  .detail.warn {
    color: var(--warn);
    white-space: normal;
  }
  @media (max-width: 720px) {
    .panel {
      left: var(--space-3);
      right: var(--space-3);
      width: auto;
      bottom: calc(4.5rem + env(safe-area-inset-bottom));
    }
  }
</style>
