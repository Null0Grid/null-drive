<script lang="ts">
  import { fileKind, type FileKind } from '../util/format';
  import Icon, { type IconName } from './Icon.svelte';

  interface Props {
    name: string;
    isDir?: boolean;
    size?: number;
  }
  let { name, isDir = false, size = 20 }: Props = $props();

  const map: Record<FileKind, IconName> = {
    folder: 'folder', image: 'image', video: 'video', audio: 'audio', archive: 'archive',
    code: 'code', text: 'text', pdf: 'pdf', doc: 'doc', sheet: 'sheet', file: 'file',
  };
  const kind = $derived(fileKind(name, isDir));
</script>

<span class="fi {kind}"><Icon name={map[kind]} {size} /></span>

<style>
  .fi {
    display: inline-flex;
    color: var(--text-faint);
  }
  .folder {
    color: var(--primary);
  }
  .image,
  .video {
    color: var(--accent);
  }
  .pdf {
    color: var(--danger);
  }
  .archive {
    color: var(--warn);
  }
  .code,
  .sheet {
    color: var(--ok);
  }
</style>
