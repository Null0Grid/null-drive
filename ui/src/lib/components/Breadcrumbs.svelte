<script lang="ts">
  import Icon from './Icon.svelte';

  interface Props {
    /** Current folder path ("" = root). */
    path: string;
    rootLabel?: string;
    /** Called with the folder path to open ("" for root). */
    onnavigate: (path: string) => void;
    /** Long paths collapse the middle into "…" beyond this many segments. */
    maxVisible?: number;
  }
  let { path, rootLabel = 'My Drive', onnavigate, maxVisible = 4 }: Props = $props();

  type Crumb = { label: string; path: string } | { ellipsis: true; hidden: { label: string; path: string }[] };

  const crumbs = $derived.by((): Crumb[] => {
    const parts = path ? path.split('/') : [];
    const all = parts.map((label, i) => ({ label, path: parts.slice(0, i + 1).join('/') }));
    const list: Crumb[] = [{ label: rootLabel, path: '' }];
    if (all.length > maxVisible) {
      list.push({ ellipsis: true, hidden: all.slice(0, all.length - (maxVisible - 1)) });
      list.push(...all.slice(all.length - (maxVisible - 1)));
    } else {
      list.push(...all);
    }
    return list;
  });
</script>

<nav aria-label="Folder path">
  <ol>
    {#each crumbs as c, i (i)}
      <li>
        {#if i > 0}<span class="sep" aria-hidden="true"><Icon name="chevron-right" size={14} /></span>{/if}
        {#if 'ellipsis' in c}
          <button
            type="button"
            class="crumb"
            title={c.hidden.map((h) => h.label).join(' / ')}
            aria-label="Show hidden folders: {c.hidden.map((h) => h.label).join(', ')}"
            onclick={() => onnavigate(c.hidden[c.hidden.length - 1]!.path)}>…</button
          >
        {:else if i === crumbs.length - 1}
          <span class="crumb current" aria-current="page">
            {#if i === 0}<Icon name="home" size={15} />{/if}
            {c.label}
          </span>
        {:else}
          <button type="button" class="crumb" onclick={() => onnavigate(c.path)}>
            {#if i === 0}<Icon name="home" size={15} />{/if}
            {c.label}
          </button>
        {/if}
      </li>
    {/each}
  </ol>
</nav>

<style>
  ol {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1);
    list-style: none;
    margin: 0;
    padding: 0;
    min-width: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    min-width: 0;
  }
  .sep {
    color: var(--text-faint);
    display: flex;
  }
  .crumb {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    max-width: 18ch;
    padding: var(--space-1) var(--space-2);
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-muted);
    font-size: var(--text-md);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  button.crumb {
    cursor: pointer;
  }
  button.crumb:hover {
    color: var(--primary);
    background: var(--primary-soft);
  }
  .current {
    color: var(--text);
    font-weight: 500;
    max-width: 30ch;
  }
</style>
