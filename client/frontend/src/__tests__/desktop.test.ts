import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import Setup from '../pages/Setup.svelte';
import StatusCard from '../components/StatusCard.svelte';
import ConflictList from '../components/ConflictList.svelte';
import ActivityList from '../components/ActivityList.svelte';
import { errText, mock } from '../bridge';
import type { Status } from '../types';

const status = (patch: Partial<Status> = {}): Status => ({
  state: 'idle', message: 'Up to date', last_sync: '', progress: { done: 0, total: 0, path: '' },
  conflicts: 0, failed: 0, skipped: [], ...patch,
});
const noop = () => {};

describe('Setup flow (demo backend)', () => {
  it('manual address → password → device, and connects', async () => {
    const ondone = vi.fn();
    render(Setup, { ondone });
    const user = userEvent.setup();

    // Empty address is rejected.
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('enter the server address');

    await user.type(screen.getByLabelText('Or enter an address'), 'nas.local:8080');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    expect(await screen.findByText('http://nas.local:8080')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Sign in with password/ }));

    await user.type(screen.getByLabelText('Username'), 'admin');
    await user.type(screen.getByLabelText('Password', { selector: 'input' }), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    // Defaults come from the backend.
    expect(screen.getByLabelText('Computer name')).toHaveValue('Desktop PC');
    expect(screen.getByLabelText('Folder to sync')).toHaveValue('/home/you/NullDrive');

    // A wrong password sends the user back to the account step.
    await user.click(screen.getByRole('button', { name: 'Connect and sync' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('wrong username or password');
    expect(screen.getByLabelText('Username')).toBeInTheDocument();

    const pw = screen.getByLabelText('Password', { selector: 'input' });
    await user.clear(pw);
    await user.type(pw, 'demo-password');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.click(screen.getByRole('button', { name: 'Connect and sync' }));
    await waitFor(() => expect(ondone).toHaveBeenCalled(), { timeout: 3000 });
    expect(mock.state.configured).toBe(true);
  });

  it('lists LAN servers and pairs with a code', async () => {
    const ondone = vi.fn();
    render(Setup, { ondone });
    const user = userEvent.setup();
    const nas = await screen.findByRole('button', { name: /nas 192\.168\.1\.20/ });
    // The second server advertises setup=pending.
    expect(screen.getByText('Not set up')).toBeInTheDocument();
    await user.click(nas);
    await user.click(await screen.findByRole('button', { name: /Pair with a code/ }));

    await user.type(screen.getByLabelText('Pairing code'), 'NULL-0000-00');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.click(screen.getByRole('button', { name: 'Pair and sync' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('not valid or has expired');
    // Sent back to the code step to fix it.
    const input = screen.getByLabelText('Pairing code');
    await user.clear(input);
    await user.type(input, 'null 7k2q 9f');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.click(screen.getByRole('button', { name: 'Pair and sync' }));
    await waitFor(() => expect(ondone).toHaveBeenCalled());
    expect(mock.state.config.server_url).toBe('http://192.168.1.20:8080');
  });

  it('explains an empty network', async () => {
    const saved = mock.discovered;
    mock.discovered = [];
    render(Setup, { ondone: noop });
    expect(await screen.findByText(/doesn't cross VLANs/)).toBeInTheDocument();
    mock.discovered = saved;
  });

  it('asks before trusting a self-signed certificate', async () => {
    render(Setup, { ondone: noop });
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('Or enter an address'), 'https://nas.lan');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    expect(await screen.findByRole('heading', { name: /Trust this server's certificate/ })).toBeInTheDocument();
    expect(screen.getByText(/^3F:A9:0C/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Trust this certificate' }));
    expect(await screen.findByText(/certificate pinned/)).toBeInTheDocument();
  });

  it('warns about plain HTTP over the internet', async () => {
    render(Setup, { ondone: noop });
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('Or enter an address'), 'drive.example.com');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('not encrypted');
  });
});

describe('StatusCard', () => {
  it('shows whether push is live', () => {
    const { unmount } = render(StatusCard, { status: status({ live: true }), onsync: noop, onpause: noop, onresume: noop, onopen: noop, onconfirm: noop });
    expect(screen.getByText('Live')).toBeInTheDocument();
    unmount();
    render(StatusCard, { status: status({ live: false }), onsync: noop, onpause: noop, onresume: noop, onopen: noop, onconfirm: noop });
    expect(screen.getByText('Polling')).toBeInTheDocument();
  });

  it('shows state and wires the buttons', async () => {
    const onsync = vi.fn();
    const onpause = vi.fn();
    render(StatusCard, { status: status(), onsync, onpause, onresume: noop, onopen: noop, onconfirm: noop });
    expect(screen.getByRole('heading', { name: 'Up to date' })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Sync now' }));
    await userEvent.click(screen.getByRole('button', { name: 'Pause' }));
    expect(onsync).toHaveBeenCalledOnce();
    expect(onpause).toHaveBeenCalledOnce();
  });

  it('paused offers Resume', async () => {
    const onresume = vi.fn();
    render(StatusCard, { status: status({ state: 'paused', message: 'Paused' }), onsync: noop, onpause: noop, onresume, onopen: noop, onconfirm: noop });
    await userEvent.click(screen.getByRole('button', { name: 'Resume' }));
    expect(onresume).toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Sync now' })).toBeNull();
  });

  it('shows progress while syncing', () => {
    render(StatusCard, {
      status: status({ state: 'syncing', message: 'Syncing…', progress: { done: 3, total: 12, path: 'Photos/a.jpg' } }),
      onsync: noop, onpause: noop, onresume: noop, onopen: noop, onconfirm: noop,
    });
    expect(screen.getByRole('progressbar', { name: 'Sync progress' })).toHaveAttribute('aria-valuenow', '25');
    expect(screen.getByText('Photos/a.jpg')).toBeInTheDocument();
  });

  it('mass-delete guard asks before deleting on the server', async () => {
    const onconfirm = vi.fn();
    render(StatusCard, {
      status: status({ state: 'attention', message: 'paused', mass_delete: { Side: 'the server', Count: 40, Total: 50 } }),
      onsync: noop, onpause: noop, onresume: noop, onopen: noop, onconfirm,
    });
    expect(screen.getByRole('alert')).toHaveTextContent('40 of 50 files are missing');
    await userEvent.click(screen.getByRole('button', { name: 'Delete them on the server' }));
    expect(onconfirm).toHaveBeenCalled();
  });
});

describe('ConflictList', () => {
  const c = { id: 7, path: 'plan.txt', conflict_path: 'plan (conflict - Laptop - 2026-10-14 2315).txt', device: 'Laptop', at: '2026-10-14T21:15:00Z' };

  it('keep both resolves directly; destructive choices confirm first', async () => {
    const onresolve = vi.fn().mockResolvedValue(undefined);
    render(ConflictList, { conflicts: [c], onresolve, onreveal: noop });
    await userEvent.click(screen.getByRole('button', { name: 'Keep both' }));
    expect(onresolve).toHaveBeenLastCalledWith(7, 'keep-both');

    await userEvent.click(screen.getByRole('button', { name: 'Use my copy' }));
    const dlg = screen.getByRole('dialog', { name: 'Use your copy?' });
    expect(dlg).toHaveTextContent('plan.txt');
    await userEvent.click(screen.getAllByRole('button', { name: 'Use my copy' }).at(-1)!);
    expect(onresolve).toHaveBeenLastCalledWith(7, 'keep-mine');
  });

  it('empty state', () => {
    render(ConflictList, { conflicts: [], onresolve: vi.fn(), onreveal: noop });
    expect(screen.getByRole('heading', { name: 'No conflicts' })).toBeInTheDocument();
  });
});

describe('ActivityList', () => {
  it('labels events and lists unsyncable items', async () => {
    const onreveal = vi.fn();
    render(ActivityList, { props: {
      events: [
        { time: new Date().toISOString(), kind: 'move-remote', path: 'b.txt', from: 'a.txt' },
        { time: new Date().toISOString(), kind: 'upload', path: 'x.txt', error: 'disk full' },
      ],
      skipped: [{ path: 'bad:name.txt', reason: 'name is not allowed on every platform' }],
      onreveal,
    } });
    expect(screen.getByText(/Moved/)).toHaveTextContent('from a.txt');
    expect(screen.getByText(/Failed: disk full/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /1 item can't be synced/ }));
    expect(screen.getByText('bad:name.txt')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'b.txt' }));
    expect(onreveal).toHaveBeenCalledWith('b.txt');
  });
});

describe('errText', () => {
  it('handles Wails string errors and Error objects', () => {
    expect(errText('boom')).toBe('boom');
    expect(errText(new Error('bang'))).toBe('bang');
  });
});

describe('history links', () => {
  it('activity rows open a file\'s history in the web app', async () => {
    const onhistory = vi.fn();
    render(ActivityList, {
      props: {
        events: [
          { time: new Date().toISOString(), kind: 'upload', path: 'Docs/report.pdf' },
          { time: new Date().toISOString(), kind: 'delete-local', path: 'old.txt' },
        ],
        skipped: [],
        onreveal: noop,
        onhistory,
      },
    });
    await userEvent.click(screen.getByRole('button', { name: 'Version history of Docs/report.pdf' }));
    expect(onhistory).toHaveBeenCalledWith('Docs/report.pdf');
    expect(screen.queryByRole('button', { name: 'Version history of old.txt' })).toBeNull();
  });

  it('shows bytes for the file being transferred', () => {
    render(StatusCard, {
      status: status({ state: 'syncing', progress: { done: 1, total: 3, path: 'big.iso', bytes: 5 << 20, bytes_total: 20 << 20 } }),
      onsync: noop, onpause: noop, onresume: noop, onopen: noop, onconfirm: noop,
    });
    expect(screen.getByText(/5(\.0)? MB of 20(\.0)? MB/)).toBeInTheDocument();
  });
});

describe('End-to-end encryption unlock', () => {
  it('asks for the passphrase and reports a wrong one', async () => {
    const { default: UnlockCard } = await import('../components/UnlockCard.svelte');
    const onunlocked = vi.fn();
    render(UnlockCard, { mode: 'locked', onunlocked });
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('Encryption passphrase'), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Unlock and sync' }));
    expect(await screen.findByRole('alert')).toHaveTextContent("doesn't match");
    expect(onunlocked).not.toHaveBeenCalled();
    await user.clear(screen.getByLabelText('Encryption passphrase'));
    await user.type(screen.getByLabelText('Encryption passphrase'), 'correct horse battery');
    await user.click(screen.getByRole('button', { name: 'Unlock and sync' }));
    await waitFor(() => expect(onunlocked).toHaveBeenCalled());
    expect(mock.state.e2ee).toBe('unlocked');
  });

  it('can switch to the recovery key, and explains a migration in progress', async () => {
    const { default: UnlockCard } = await import('../components/UnlockCard.svelte');
    const r = render(UnlockCard, { mode: 'locked', onunlocked: noop });
    await userEvent.click(screen.getByRole('button', { name: 'Use the recovery key' }));
    expect(screen.getByLabelText('Recovery key')).toBeInTheDocument();
    r.unmount();
    render(UnlockCard, { mode: 'migrating', onunlocked: noop });
    expect(screen.getByText(/being turned on/)).toBeInTheDocument();
  });
});
