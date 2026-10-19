import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import HistoryDialog from '../components/HistoryDialog.svelte';
import Trash from '../pages/Trash.svelte';
import { client, drive } from '../api';
import { parse } from '../router.svelte';
import type { FileVersion } from '../../lib/api/types';

afterEach(() => vi.restoreAllMocks());

const v = (version: number, patch: Partial<FileVersion> = {}): FileVersion => ({
  version, size: 1000 * version, sha256: 'h' + version, created_at: new Date(Date.now() - version * 3600_000).toISOString(),
  origin: 'upload', chunks: 1, current: false, ...patch,
});

describe('routes', () => {
  it('parses /trash and ?history=', () => {
    expect(parse('/trash')).toEqual({ name: 'trash' });
    expect(parse('/files/Docs', '?history=a%20b.txt')).toEqual({ name: 'files', folder: 'Docs', history: 'a b.txt' });
    expect(parse('/files', '')).toEqual({ name: 'files', folder: '' });
  });
});

describe('HistoryDialog', () => {
  it('lists versions with devices and restores an old one', async () => {
    const list = vi.spyOn(client, 'listVersions').mockResolvedValue({
      path: 'Docs/report.txt', deleted: false,
      versions: [v(3, { current: true, device: 'Laptop' }), v(2, { origin: 'restore' }), v(1, { device: 'Desktop PC' })],
    });
    const restore = vi.spyOn(client, 'restoreVersion').mockResolvedValue({} as never);
    const onrestored = vi.fn();
    render(HistoryDialog, { path: 'Docs/report.txt', onclose: () => {}, onrestored });

    const items = await screen.findAllByRole('listitem');
    expect(items).toHaveLength(3);
    expect(within(items[0]!).getByText('Current')).toBeInTheDocument();
    expect(within(items[0]!).queryByRole('button', { name: /Restore/ })).toBeNull();
    expect(within(items[1]!).getByText('Restored')).toBeInTheDocument();
    expect(within(items[1]!).getByText(/^Web/)).toBeInTheDocument();
    expect(within(items[2]!).getByText(/Desktop PC/)).toBeInTheDocument();
    // Download goes through the Drive (a link for plain files, decrypted
    // in the browser for encrypted ones).
    const dl = vi.spyOn(drive, 'download').mockResolvedValue();
    await userEvent.click(within(items[2]!).getByRole('button', { name: /Download version/ }));
    expect(dl).toHaveBeenCalledWith('Docs/report.txt', 'report.txt', 1);

    await userEvent.click(within(items[2]!).getByRole('button', { name: /Restore/ }));
    expect(screen.getByText(/nothing in the history is lost/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Restore' }));
    expect(restore).toHaveBeenCalledWith('Docs/report.txt', 1);
    expect(onrestored).toHaveBeenCalled();
    expect(list).toHaveBeenCalledTimes(2); // reloaded after the restore
  });

  it('explains a deleted file', async () => {
    vi.spyOn(client, 'listVersions').mockResolvedValue({ path: 'x.txt', deleted: true, versions: [v(1)] });
    render(HistoryDialog, { path: 'x.txt', onclose: () => {} });
    expect(await screen.findByText(/This file is deleted/)).toBeInTheDocument();
  });
});

describe('Trash page', () => {
  it('lists deleted files and restores one', async () => {
    const now = Date.now();
    vi.spyOn(client, 'trash')
      .mockResolvedValueOnce([
        { path: 'Photos/beach.jpg', size: 2_400_000, deleted_at: new Date(now - 86_400_000).toISOString(), version: 9, versions: 3,
          purge_at: new Date(now + 119 * 86_400_000).toISOString() },
        { path: 'notes.txt', size: 12, deleted_at: new Date(now).toISOString(), version: 4, versions: 1,
          purge_at: new Date(now + 120 * 86_400_000).toISOString() },
      ])
      .mockResolvedValue([]);
    const restore = vi.spyOn(client, 'restoreVersion').mockResolvedValue({} as never);
    render(Trash);
    expect(await screen.findByText('beach.jpg')).toBeInTheDocument();
    expect(screen.getByText(/^Photos/)).toBeInTheDocument();
    expect(screen.getByText('119 days left')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Versions of beach.jpg' })).toHaveTextContent('3 versions');
    expect(screen.queryByRole('button', { name: 'Versions of notes.txt' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Restore beach.jpg' }));
    expect(restore).toHaveBeenCalledWith('Photos/beach.jpg', 9);
    expect(await screen.findByText('Nothing here')).toBeInTheDocument();
  });
});
