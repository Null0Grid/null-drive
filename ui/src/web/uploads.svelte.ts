// Upload queue: runs uploads with limited concurrency, tracks progress, and
// applies the "never silently lose data" rule when the server reports a
// conflict mid-upload (the file changed or appeared since we looked): the
// upload is retried under a "name (1).ext" name instead of failing or
// overwriting.

import { ApiError, type NullDriveClient } from '../lib/api/client';

/** What the queue needs: the API client, or the encrypting Drive. */
export type Uploader = Pick<NullDriveClient, 'upload'>;
import type { FileEntry } from '../lib/api/types';
import { uniqueName } from '../lib/util/names';
import { basename, dirname, join } from '../lib/util/paths';

export type UploadStatus = 'queued' | 'uploading' | 'done' | 'error' | 'cancelled';

export interface UploadJob {
  id: number;
  file: File;
  /** Destination path on the server. */
  path: string;
  /** Replace an existing file at this version (If-Match). */
  ifMatch?: number;
  loaded: number;
  status: UploadStatus;
  error?: string;
  /** Set when a conflict made us save under another name. */
  renamedTo?: string;
}

const CONCURRENCY = 3;
let nextId = 1;

export class UploadQueue {
  jobs = $state<UploadJob[]>([]);
  private controllers = new Map<number, AbortController>();
  private running = 0;

  /** Progress over all jobs that are not finished-and-cleared. */
  totals = $derived.by(() => {
    let total = 0;
    let loaded = 0;
    let active = 0;
    let failed = 0;
    for (const j of this.jobs) {
      if (j.status === 'cancelled') continue;
      total += j.file.size;
      loaded += j.status === 'done' ? j.file.size : j.loaded;
      if (j.status === 'queued' || j.status === 'uploading') active++;
      if (j.status === 'error') failed++;
    }
    return { total, loaded, active, failed, fraction: total ? loaded / total : active ? 0 : 1 };
  });

  constructor(
    private client: Uploader,
    /** Current listing, used to pick a free name after a conflict. */
    private takenNames: (folder: string) => Set<string>,
    private onUploaded: (entry: FileEntry, job: UploadJob) => void,
    private onDrained: () => void,
  ) {}

  add(items: { file: File; path: string; ifMatch?: number }[]) {
    for (const it of items) {
      this.jobs.push({ id: nextId++, file: it.file, path: it.path, ifMatch: it.ifMatch, loaded: 0, status: 'queued' });
    }
    this.pump();
  }

  cancel(id: number) {
    const job = this.jobs.find((j) => j.id === id);
    if (!job) return;
    if (job.status === 'queued') job.status = 'cancelled';
    this.controllers.get(id)?.abort();
  }

  retry(id: number) {
    const job = this.jobs.find((j) => j.id === id);
    if (job && (job.status === 'error' || job.status === 'cancelled')) {
      job.status = 'queued';
      job.loaded = 0;
      job.error = undefined;
      this.pump();
    }
  }

  clearFinished() {
    this.jobs = this.jobs.filter((j) => j.status === 'queued' || j.status === 'uploading');
  }

  private pump() {
    while (this.running < CONCURRENCY) {
      const job = this.jobs.find((j) => j.status === 'queued');
      if (!job) break;
      this.running++;
      void this.run(job).finally(() => {
        this.running--;
        this.pump();
        if (this.running === 0 && !this.jobs.some((j) => j.status === 'queued')) this.onDrained();
      });
    }
  }

  private async run(job: UploadJob) {
    job.status = 'uploading';
    const ctl = new AbortController();
    this.controllers.set(job.id, ctl);
    try {
      const entry = await this.uploadOnce(job, ctl.signal);
      job.status = 'done';
      job.loaded = job.file.size;
      this.onUploaded(entry, job);
    } catch (e) {
      if (e instanceof ApiError && e.code === 'aborted') {
        job.status = 'cancelled';
      } else {
        job.status = 'error';
        job.error = e instanceof Error ? e.message : String(e);
      }
    } finally {
      this.controllers.delete(job.id);
    }
  }

  private async uploadOnce(job: UploadJob, signal: AbortSignal): Promise<FileEntry> {
    const opts = { ifMatch: job.ifMatch, signal, onProgress: (l: number) => (job.loaded = l) };
    try {
      return await this.client.upload(job.path, job.file, opts);
    } catch (e) {
      // 409 conflict (changed since we looked), 428 (appeared since we
      // looked) or 409 path_collision (a folder took the name): keep both.
      const keepBoth =
        e instanceof ApiError && (e.code === 'conflict' || e.code === 'precondition_required' || e.code === 'path_collision');
      if (!keepBoth) throw e;
      const folder = dirname(job.path);
      const taken = this.takenNames(folder);
      taken.add(basename(job.path));
      const alt = join(folder, uniqueName(basename(job.path), taken));
      job.renamedTo = alt;
      job.loaded = 0;
      return await this.client.upload(alt, job.file, { ...opts, ifMatch: 0 });
    }
  }
}
