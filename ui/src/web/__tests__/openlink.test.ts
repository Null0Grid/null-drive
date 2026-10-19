import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import OpenLink from '../pages/OpenLink.svelte';
import { drive } from '../api';
import { parse, router } from '../router.svelte';
import { ApiError } from '../../lib/api/client';
import type { FileEntry } from '../../lib/api/types';

afterEach(() => vi.restoreAllMocks());

const ID = 'd28fc8c06bfa5b3229e9cada9b33e866';
const entry = (patch: Partial<FileEntry>): FileEntry => ({
  id: ID, path: 'Docs/Q3 report.pdf', is_dir: false, size: 1, sha256: 'x', version: 1, modified_at: '', deleted: false, ...patch,
});

describe('routes', () => {
  it('parses /open?id= and ?show=', () => {
    expect(parse('/open', `?id=${ID}`)).toEqual({ name: 'open', id: ID });
    expect(parse('/open', `?id=${ID.toUpperCase()}`)).toEqual({ name: 'open', id: ID });
    for (const bad of ['', '?id=', '?id=xyz', `?id=${ID}0`, '?id=../../etc']) expect(parse('/open', bad)).toEqual({ name: 'notfound' });
    expect(parse('/files/Docs', '?show=Q3%20report.pdf')).toEqual({ name: 'files', folder: 'Docs', show: 'Q3 report.pdf' });
  });
});

describe('OpenLink', () => {
  it('shows a file in its folder', async () => {
    vi.spyOn(drive, 'fileById').mockResolvedValue(entry({}));
    const nav = vi.spyOn(router, 'navigate').mockImplementation(() => {});
    render(OpenLink, { id: ID });
    await vi.waitFor(() => expect(nav).toHaveBeenCalledWith('/files/Docs?show=Q3%20report.pdf', { replace: true }));
  });

  it('opens a folder, and a file at the top level', async () => {
    const nav = vi.spyOn(router, 'navigate').mockImplementation(() => {});
    vi.spyOn(drive, 'fileById').mockResolvedValueOnce(entry({ path: 'Docs/Sub', is_dir: true })).mockResolvedValueOnce(entry({ path: 'a.txt' }));
    render(OpenLink, { id: ID });
    await vi.waitFor(() => expect(nav).toHaveBeenCalledWith('/files/Docs/Sub', { replace: true }));
    render(OpenLink, { id: ID });
    await vi.waitFor(() => expect(nav).toHaveBeenCalledWith('/files?show=a.txt', { replace: true }));
  });

  it('explains a deleted file and an unknown id', async () => {
    vi.spyOn(drive, 'fileById').mockResolvedValueOnce(entry({ deleted: true }));
    render(OpenLink, { id: ID });
    expect(await screen.findByText('This file was deleted')).toBeTruthy();
    expect(screen.getByText(/Q3 report.pdf/)).toBeTruthy();

    vi.spyOn(drive, 'fileById').mockRejectedValueOnce(new ApiError(404, 'not_found', 'no such file or folder'));
    render(OpenLink, { id: ID });
    expect(await screen.findByText('Not in this account')).toBeTruthy();
  });
});
