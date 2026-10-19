<script lang="ts">
  import { onMount, tick } from 'svelte';
  import Breadcrumbs from '../../lib/components/Breadcrumbs.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import EmptyState from '../../lib/components/EmptyState.svelte';
  import FileRow, { type FileItem } from '../../lib/components/FileRow.svelte';
  import FileTile from '../../lib/components/FileTile.svelte';
  import ProgressBar from '../../lib/components/ProgressBar.svelte';
  import type { MenuItem } from '../../lib/components/Menu.svelte';
  import { toast } from '../../lib/toast.svelte';
  import { ApiError } from '../../lib/api/client';
  import type { FileEntry } from '../../lib/api/types';
  import { formatBytes, isPreviewableImage } from '../../lib/util/format';
  import { uniqueName } from '../../lib/util/names';
  import {
    allFolders,
    basename,
    childrenOf,
    dirname,
    exists,
    join,
    sortItems,
    type Item,
    type SortKey,
  } from '../../lib/util/paths';
  import { client, drive } from '../api';
  import { session } from '../session.svelte';
  import { isEncryptedPath } from '../../lib/crypto/keys';
  import { folderHref, router } from '../router.svelte';
  import { errorMessage } from '../actions';
  import { collectDropped, fromInput, type DroppedFile } from '../dropped';
  import { UploadQueue } from '../uploads.svelte';
  import { live } from '../live.svelte';
  import NameDialog from '../components/NameDialog.svelte';
  import MoveDialog from '../components/MoveDialog.svelte';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import ConflictDialog, { type Resolution } from '../components/ConflictDialog.svelte';
  import UploadPanel from '../components/UploadPanel.svelte';
  import HistoryDialog from '../components/HistoryDialog.svelte';

  interface Props {
    folder: string;
    /** File name in this folder whose history to show (?history=). */
    history?: string;
    /** Entry in this folder to scroll to and highlight (?show=, from /open links). */
    show?: string;
  }
  let { folder, history, show }: Props = $props();

  // ---------- data ----------
  let entries = $state<FileEntry[]>([]);
  let loaded = $state(false);
  let loadError = $state<string | null>(null);

  async function reload() {
    try {
      const list = await drive.listFiles();
      if (!drive.encrypted && list.files.some((f) => isEncryptedPath(f.path))) {
        // Encryption was turned on from another device or tab: re-read the
        // account so the unlock screen takes over.
        const me = await client.me();
        session.user = me.user;
        return;
      }
      entries = list.files;
      loadError = null;
    } catch (e) {
      loadError = errorMessage(e);
    } finally {
      loaded = true;
    }
  }
  onMount(() => {
    void reload();
    // Another device changed something: refresh (debounced, so a burst of
    // pushes during a big sync causes one listing request, not dozens).
    let t: ReturnType<typeof setTimeout> | undefined;
    const off = live.on((m) => {
      if (m.type !== 'changed') return;
      clearTimeout(t);
      t = setTimeout(reload, 300);
    });
    return () => {
      off();
      clearTimeout(t);
    };
  });

  // ---------- view state (remembered per browser) ----------
  function pref<T extends string>(key: string, fallback: T, allowed: readonly T[]): T {
    try {
      const v = localStorage.getItem(key) as T | null;
      return v && allowed.includes(v) ? v : fallback;
    } catch {
      return fallback;
    }
  }
  function savePref(key: string, v: string) {
    try {
      localStorage.setItem(key, v);
    } catch {
      /* ignore */
    }
  }
  let view = $state(pref('nd.view', 'list', ['list', 'grid'] as const));
  let sortKey = $state<SortKey>(pref('nd.sort', 'name', ['name', 'size', 'modified'] as const));
  let sortDesc = $state(false);
  let filter = $state('');

  // Clear the filter when changing folders.
  let lastFolder = '';
  $effect(() => {
    if (folder !== lastFolder) {
      lastFolder = folder;
      filter = '';
    }
  });

  const folderExists = $derived(folder === '' || exists(entries, folder) !== undefined);
  const children = $derived(childrenOf(entries, folder));
  const items = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    const visible = q ? children.filter((i) => i.name.toLowerCase().includes(q)) : children;
    return sortItems(visible, sortKey, sortDesc);
  });
  const stats = $derived.by(() => {
    let dirs = 0;
    let files = 0;
    let bytes = 0;
    for (const i of children) {
      if (i.isDir) dirs++;
      else files++;
      bytes += i.size;
    }
    return { dirs, files, bytes };
  });

  function setSort(k: SortKey) {
    if (sortKey === k) sortDesc = !sortDesc;
    else {
      sortKey = k;
      sortDesc = k !== 'name'; // biggest / newest first feels natural
    }
    savePref('nd.sort', k);
  }
  function setView(v: 'list' | 'grid') {
    view = v;
    savePref('nd.view', v);
  }

  function namesIn(f: string): Set<string> {
    return new Set(childrenOf(entries, f).map((i) => i.name));
  }

  // ---------- actions ----------
  const folderActions: MenuItem[] = [
    { id: 'open', label: 'Open', icon: 'folder' },
    { id: 'rename', label: 'Rename', icon: 'edit' },
    { id: 'move', label: 'Move to…', icon: 'move' },
    { id: 'delete', label: 'Delete', icon: 'trash', danger: true },
  ];
  const fileActions: MenuItem[] = [
    { id: 'download', label: 'Download', icon: 'download' },
    { id: 'history', label: 'History', icon: 'refresh' },
    { id: 'rename', label: 'Rename', icon: 'edit' },
    { id: 'move', label: 'Move to…', icon: 'move' },
    { id: 'delete', label: 'Delete', icon: 'trash', danger: true },
  ];

  function open(item: FileItem) {
    if (item.isDir) router.navigate(folderHref(item.path));
    else download(item);
  }

  function download(item: FileItem) {
    // Plain files: a link with Content-Disposition: attachment. Encrypted
    // files: fetched and decrypted in the browser, then saved.
    drive.download(item.path, item.name).catch((e) => toast.error(`Couldn't download “${item.name}”: ${errorMessage(e)}`));
  }

  // ?show=<name>: once the listing is in, scroll to the entry and make it
  // glow for a moment (where a nulldrive:// link lands in the browser).
  let shown = '';
  $effect(() => {
    if (!loaded || !show) return;
    const target = folder ? `${folder}/${show}` : show;
    if (shown === target) return;
    shown = target;
    void tick().then(() => {
      const el = document.querySelector<HTMLElement>(`[data-path="${CSS.escape(target)}"]`);
      if (!el) {
        toast.error(`“${show}” isn't in this folder any more.`);
        return;
      }
      el.scrollIntoView({ block: 'center', behavior: 'smooth' });
      el.classList.add('located');
      setTimeout(() => el.classList.remove('located'), 2600);
    });
  });

  let historyOf = $state<string | null>(null);
  // ?history=<name> (e.g. from the desktop app) opens that file's history.
  $effect(() => {
    if (history) historyOf = folder ? `${folder}/${history}` : history;
  });
  function closeHistory() {
    historyOf = null;
    if (history) router.navigate(folderHref(folder), { replace: true });
  }
  let renaming = $state<Item | null>(null);
  let moving = $state<Item | null>(null);
  let deleting = $state<Item | null>(null);
  let creatingFolder = $state(false);

  function onaction(action: string, fi: FileItem) {
    const item = children.find((c) => c.path === fi.path);
    if (!item) return;
    if (action === 'open') open(item);
    else if (action === 'download') download(item);
    else if (action === 'history') historyOf = item.path;
    else if (action === 'rename') renaming = item;
    else if (action === 'move') moving = item;
    else if (action === 'delete') deleting = item;
  }

  function friendly(e: unknown): string {
    if (e instanceof ApiError) {
      if (e.code === 'target_exists') return 'Something with that name already exists there.';
      if (e.code === 'conflict') return 'It was changed on another device. Refresh and try again.';
      if (e.code === 'not_found') return 'It no longer exists (deleted elsewhere?).';
      if (e.code === 'path_collision') return 'A file and a folder cannot have the same name.';
    }
    return errorMessage(e);
  }

  async function createFolder(name: string) {
    try {
      await drive.createFolder(join(folder, name));
    } catch (e) {
      throw new Error(friendly(e));
    }
    await reload();
    toast.success(`Created folder “${name}”.`);
  }

  async function rename(name: string) {
    const item = renaming!;
    try {
      await drive.move(item.path, join(dirname(item.path), name), item.isDir || !item.version ? undefined : item.version);
    } catch (e) {
      await reload();
      throw new Error(friendly(e));
    }
    await reload();
    toast.success(`Renamed to “${name}”.`);
  }

  async function move(dest: string) {
    const item = moving!;
    if (namesIn(dest).has(item.name)) throw new Error(`“${item.name}” already exists in ${dest ? `“${basename(dest)}”` : 'My Drive'}.`);
    try {
      await drive.move(item.path, join(dest, item.name), item.isDir || !item.version ? undefined : item.version);
    } catch (e) {
      await reload();
      throw new Error(friendly(e));
    }
    await reload();
    toast.success(`Moved “${item.name}” to ${dest ? `“${basename(dest)}”` : 'My Drive'}.`, {
      action: { label: 'Open', run: () => router.navigate(folderHref(dest)) },
    });
  }

  async function remove() {
    const item = deleting!;
    try {
      await drive.remove(item.path, item.isDir || !item.version ? undefined : item.version);
      toast.success(`Deleted “${item.name}”.`);
    } catch (e) {
      toast.error(`Couldn't delete “${item.name}”: ${friendly(e)}`);
    }
    await reload();
  }

  const deleteMessage = $derived(
    deleting
      ? deleting.isDir
        ? `“${deleting.name}” and everything inside it (${deleting.count} file${deleting.count === 1 ? '' : 's'}, ${formatBytes(deleting.size)}) will be deleted from the server and from every synced device.`
        : `“${deleting.name}” will be deleted from the server and from every synced device.`
      : '',
  );

  // ---------- uploads ----------
  let reloadTimer: ReturnType<typeof setTimeout> | undefined;
  const queue = new UploadQueue(
    drive,
    (f) => namesIn(f),
    (entry) => {
      // Show finished files right away; a full refresh follows when idle.
      entries = [...entries.filter((e) => e.path !== entry.path), entry];
      clearTimeout(reloadTimer);
      reloadTimer = setTimeout(reload, 800);
    },
    () => {
      void reload();
      const t = queue.totals;
      if (t.failed === 0) toast.success('Upload complete.');
      else toast.error(`${t.failed} upload${t.failed === 1 ? '' : 's'} failed. Retry from the upload panel.`);
    },
  );

  let conflict = $state<{ names: string[]; resolve: (r: Resolution) => void } | null>(null);

  async function startUpload(files: DroppedFile[]) {
    if (files.length === 0) return;
    const target = folder;
    type Plan = { file: File; path: string; existing?: FileEntry; folderClash?: boolean };
    const plans: Plan[] = files.map(({ file, relativePath }) => {
      const path = join(target, relativePath);
      const ex = exists(entries, path);
      if (ex === undefined) return { file, path };
      if (ex === 'implicit' || ex.is_dir) return { file, path, folderClash: true };
      return { file, path, existing: ex };
    });

    const clashing = plans.filter((p) => p.existing);
    let resolution: Resolution = 'keep';
    if (clashing.length > 0) {
      resolution = await new Promise<Resolution>((resolve) => {
        conflict = { names: clashing.map((p) => p.path.slice(target ? target.length + 1 : 0)), resolve };
      });
      conflict = null;
    }

    // Names already used per folder, including names this batch will take.
    const taken = new Map<string, Set<string>>();
    const takenIn = (f: string) => {
      if (!taken.has(f)) taken.set(f, namesIn(f));
      return taken.get(f)!;
    };
    const jobs: { file: File; path: string; ifMatch?: number }[] = [];
    for (const p of plans) {
      const dir = dirname(p.path);
      if (p.existing && resolution === 'skip') continue;
      if (p.existing && resolution === 'replace') {
        jobs.push({ file: p.file, path: p.path, ifMatch: p.existing.version });
        continue;
      }
      if (p.existing || p.folderClash) {
        const set = takenIn(dir);
        const name = uniqueName(basename(p.path), set);
        set.add(name);
        jobs.push({ file: p.file, path: join(dir, name), ifMatch: 0 });
        continue;
      }
      takenIn(dir).add(basename(p.path));
      jobs.push({ file: p.file, path: p.path });
    }
    if (jobs.length === 0) {
      toast.info('Nothing to upload.');
      return;
    }
    queue.add(jobs);
  }

  let fileInput = $state<HTMLInputElement>();
  let folderInput = $state<HTMLInputElement>();
  function onpick(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    if (input.files) void startUpload(fromInput(input.files));
    input.value = '';
  }

  // Drag and drop anywhere on the page. dragenter/leave fire for every
  // child element, so count them to know when the pointer really left.
  let dragDepth = $state(0);
  const dragging = $derived(dragDepth > 0);
  const hasFiles = (e: DragEvent) => !!e.dataTransfer && Array.from(e.dataTransfer.types).includes('Files');
  function ondragenter(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth++;
  }
  function ondragleave(e: DragEvent) {
    if (!hasFiles(e)) return;
    dragDepth = Math.max(0, dragDepth - 1);
  }
  function ondragover(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.dataTransfer!.dropEffect = 'copy';
  }
  async function ondrop(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth = 0;
    try {
      await startUpload(await collectDropped(e.dataTransfer!));
    } catch (err) {
      toast.error(`Couldn't read the dropped files: ${errorMessage(err)}`);
    }
  }

  const sortOptions: { key: SortKey; label: string }[] = [
    { key: 'name', label: 'Name' },
    { key: 'size', label: 'Size' },
    { key: 'modified', label: 'Modified' },
  ];
</script>

<svelte:window {ondragenter} {ondragleave} {ondragover} {ondrop} />
<svelte:head><title>{folder ? `${basename(folder)} · ` : ''}Null Drive</title></svelte:head>

<div class="files">
  <div class="top">
    <Breadcrumbs path={folder} onnavigate={(p) => router.navigate(folderHref(p))} />
    <div class="actions">
      <Button icon="folder-plus" onclick={() => (creatingFolder = true)} disabled={!folderExists}>New folder</Button>
      <Button variant="primary" icon="upload" onclick={() => fileInput?.click()} disabled={!folderExists}>Upload</Button>
    </div>
  </div>

  <div class="toolbar">
    <div class="filter">
      <Input label="Filter by name" hideLabel placeholder="Filter this folder…" icon="search" bind:value={filter} type="search" />
    </div>
    <div class="right">
      <label class="sort">
        <span class="sr-only">Sort by</span>
        <select
          value={sortKey}
          onchange={(e) => {
            const k = e.currentTarget.value as SortKey;
            sortKey = k;
            sortDesc = k !== 'name';
            savePref('nd.sort', k);
          }}
        >
          {#each sortOptions as o (o.key)}<option value={o.key}>{o.label}</option>{/each}
        </select>
      </label>
      <Button
        variant="ghost"
        iconOnly
        icon={sortDesc ? 'arrow-down' : 'arrow-up'}
        label={sortDesc ? 'Sorted descending' : 'Sorted ascending'}
        onclick={() => (sortDesc = !sortDesc)}
      />
      <div class="seg" role="group" aria-label="View">
        <Button variant="ghost" iconOnly icon="list" label="List view" aria-pressed={view === 'list'} onclick={() => setView('list')} />
        <Button variant="ghost" iconOnly icon="grid" label="Grid view" aria-pressed={view === 'grid'} onclick={() => setView('grid')} />
      </div>
      <Button variant="ghost" iconOnly icon="folder" label="Upload a folder" onclick={() => folderInput?.click()} disabled={!folderExists} class="folder-upload" />
    </div>
  </div>

  <input bind:this={fileInput} type="file" multiple hidden onchange={onpick} data-testid="file-input" />
  <input bind:this={folderInput} type="file" multiple hidden onchange={onpick} webkitdirectory />

  {#if !loaded}
    <div class="loading"><ProgressBar label="Loading files" /></div>
  {:else if loadError}
    <EmptyState icon="alert" tone="danger" title="Couldn't load your files" message={loadError}>
      <Button variant="primary" icon="refresh" onclick={reload}>Try again</Button>
    </EmptyState>
  {:else if !folderExists}
    <EmptyState icon="folder" title="Folder not found" message="It may have been moved, renamed or deleted on another device.">
      <Button variant="primary" icon="home" onclick={() => router.navigate('/files')}>Go to My Drive</Button>
    </EmptyState>
  {:else if children.length === 0}
    <EmptyState icon="upload" title={folder ? 'This folder is empty' : 'Your drive is empty'} message="Drag files or folders here, or use the Upload button.">
      <Button variant="primary" icon="upload" onclick={() => fileInput?.click()}>Upload files</Button>
      <Button icon="folder-plus" onclick={() => (creatingFolder = true)}>New folder</Button>
    </EmptyState>
  {:else if items.length === 0}
    <EmptyState icon="search" title="No matches" message="Nothing in this folder matches “{filter}”.">
      <Button onclick={() => (filter = '')}>Clear filter</Button>
    </EmptyState>
  {:else if view === 'list'}
    <div class="list" role="list" aria-label="Files in {folder ? basename(folder) : 'My Drive'}">
      <div class="list-head" aria-hidden="true">
        {#each sortOptions as o (o.key)}
          <button type="button" class="col {o.key}" class:active={sortKey === o.key} tabindex="-1" onclick={() => setSort(o.key)}>
            {o.label}
            {#if sortKey === o.key}<Icon name={sortDesc ? 'arrow-down' : 'arrow-up'} size={12} />{/if}
          </button>
        {/each}
        <span></span>
      </div>
      {#each items as item (item.path)}
        <div role="listitem">
          <FileRow {item} highlight={filter} actions={item.isDir ? folderActions : fileActions} onopen={open} {onaction} />
        </div>
      {/each}
    </div>
  {:else}
    <div class="grid" role="list" aria-label="Files in {folder ? basename(folder) : 'My Drive'}">
      {#each items as item (item.path)}
        <div role="listitem">
          <FileTile
            {item}
            actions={item.isDir ? folderActions : fileActions}
            onopen={open}
            {onaction}
            thumbnail={!item.isDir && isPreviewableImage(item.name) && item.size < 15 * 1024 * 1024 ? drive.thumbnailUrl(item.path) : undefined}
          />
        </div>
      {/each}
    </div>
  {/if}

  {#if loaded && !loadError && folderExists && children.length > 0}
    <p class="hud" aria-live="polite">
      &gt; {stats.dirs} folder{stats.dirs === 1 ? '' : 's'} · {stats.files} file{stats.files === 1 ? '' : 's'} · {formatBytes(stats.bytes)}{#if filter}&nbsp;· {items.length} shown{/if}
    </p>
  {/if}
</div>

{#if dragging}
  <div class="drop" aria-hidden="true">
    <div class="drop-inner">
      <Icon name="upload" size={40} />
      <p>Drop to upload to <strong>{folder ? basename(folder) : 'My Drive'}</strong></p>
    </div>
  </div>
{/if}

<UploadPanel {queue} />

<NameDialog
  open={creatingFolder}
  title="New folder"
  label="Folder name"
  initial="New folder"
  confirmLabel="Create"
  taken={new Set(children.map((c) => c.name))}
  onsubmit={createFolder}
  onclose={() => (creatingFolder = false)}
/>
<NameDialog
  open={renaming !== null}
  title="Rename"
  label="New name"
  initial={renaming?.name ?? ''}
  confirmLabel="Rename"
  selectStem={!renaming?.isDir}
  taken={new Set(children.map((c) => c.name))}
  onsubmit={rename}
  onclose={() => (renaming = null)}
/>
<MoveDialog open={moving !== null} source={moving} folders={allFolders(entries)} onmove={move} onclose={() => (moving = null)} />
<ConfirmDialog
  open={deleting !== null}
  title={deleting?.isDir ? 'Delete folder?' : 'Delete file?'}
  message={deleteMessage + ' You can restore it from Recently deleted.'}
  confirmLabel="Delete"
  danger
  onconfirm={remove}
  onclose={() => (deleting = null)}
/>
<HistoryDialog path={historyOf} onclose={closeHistory} onrestored={reload} />
<ConflictDialog open={conflict !== null} names={conflict?.names ?? []} onresolve={(r) => conflict?.resolve(r)} />

<style>
  .files {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    max-width: 72rem;
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    flex-wrap: wrap;
    min-height: 2.5rem;
  }
  .actions {
    display: flex;
    gap: var(--space-2);
  }
  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
  }
  .filter {
    flex: 1;
    max-width: 22rem;
  }
  .right {
    display: flex;
    align-items: center;
    gap: var(--space-1);
  }
  select {
    height: 2.375rem;
    padding: 0 var(--space-3);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-md);
    background: var(--surface);
    color: var(--text);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .seg {
    display: flex;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 1px;
  }
  .seg :global(.btn[aria-pressed='true']) {
    background: var(--primary-soft);
    color: var(--primary);
  }
  .loading {
    padding: var(--space-6) 0;
  }
  .list {
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    background: var(--panel);
    overflow: visible;
  }
  .list > div[role='listitem']:last-child :global(.row) {
    border-bottom: 0;
  }
  .list-head {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 7rem 8rem var(--tap);
    gap: var(--space-3);
    padding: 0 var(--space-2) 0 calc(var(--space-3) + 20px + var(--space-3));
    border-bottom: 1px solid var(--border);
    background: var(--bg-sunken);
    border-radius: var(--radius-lg) var(--radius-lg) 0 0;
  }
  .col {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    height: 2.25rem;
    padding: 0;
    border: 0;
    background: none;
    color: var(--text-faint);
    font-size: var(--text-xs);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    cursor: pointer;
  }
  .col.size {
    justify-content: flex-end;
  }
  .col.active,
  .col:hover {
    color: var(--primary);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(10.5rem, 1fr));
    gap: var(--space-3);
  }
  .hud {
    font-family: var(--font-term);
    font-size: 1.15rem;
    color: var(--text-faint);
  }
  .drop {
    position: fixed;
    inset: 0;
    z-index: 45;
    display: grid;
    place-items: center;
    padding: var(--space-5);
    background: var(--backdrop);
    pointer-events: none;
  }
  .drop-inner {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-3);
    width: min(32rem, 100%);
    padding: var(--space-7) var(--space-5);
    border: 2px dashed var(--primary);
    border-radius: var(--radius-lg);
    background: var(--panel);
    color: var(--primary);
    box-shadow: var(--glow-primary);
    text-align: center;
  }
  .drop-inner p {
    color: var(--text);
  }

  @media (max-width: 720px) {
    .list-head {
      display: none;
    }
    .top {
      flex-direction: column;
      align-items: stretch;
    }
    .actions :global(.btn) {
      flex: 1;
    }
    .toolbar {
      flex-wrap: wrap;
    }
    .filter {
      max-width: none;
      flex-basis: 100%;
    }
    .right {
      width: 100%;
      justify-content: flex-end;
    }
    .right :global(.folder-upload) {
      display: none;
    }
    .grid {
      grid-template-columns: repeat(auto-fill, minmax(8.5rem, 1fr));
      gap: var(--space-2);
    }
  }
</style>
