import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet, flushSync } from 'svelte';
import Button from '../Button.svelte';
import Breadcrumbs from '../Breadcrumbs.svelte';
import EmptyState from '../EmptyState.svelte';
import FileRow from '../FileRow.svelte';
import FileTile from '../FileTile.svelte';
import Input from '../Input.svelte';
import Modal from '../Modal.svelte';
import ProgressBar from '../ProgressBar.svelte';
import Sidebar from '../Sidebar.svelte';
import ToastHost from '../ToastHost.svelte';
import PasswordStrength from '../PasswordStrength.svelte';
import { toast } from '../../toast.svelte';

const text = (s: string) => createRawSnippet(() => ({ render: () => `<span>${s}</span>` }));

describe('Button', () => {
  it('renders a label and handles clicks', async () => {
    const onclick = vi.fn();
    render(Button, { label: 'Save', variant: 'primary', onclick });
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(onclick).toHaveBeenCalledOnce();
  });
  it('icon-only buttons are labelled for screen readers', () => {
    render(Button, { icon: 'trash', iconOnly: true, label: 'Delete' });
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveAttribute('title', 'Delete');
  });
  it('loading disables the button and marks it busy', async () => {
    const onclick = vi.fn();
    render(Button, { label: 'Go', loading: true, onclick });
    const b = screen.getByRole('button', { name: 'Go' });
    expect(b).toBeDisabled();
    expect(b).toHaveAttribute('aria-busy', 'true');
    await userEvent.click(b);
    expect(onclick).not.toHaveBeenCalled();
  });
  it('defaults to type=button so it never submits forms by accident', () => {
    render(Button, { label: 'X' });
    expect(screen.getByRole('button')).toHaveAttribute('type', 'button');
  });
});

describe('Input', () => {
  it('associates label, hint and error', () => {
    render(Input, { label: 'Username', hint: 'Letters only', value: '' });
    const input = screen.getByLabelText('Username');
    expect(input).toHaveAccessibleDescription('Letters only');
    expect(input).not.toHaveAttribute('aria-invalid');
  });
  it('marks errors as invalid and announces them', () => {
    render(Input, { label: 'Username', error: 'Required' });
    expect(screen.getByLabelText('Username')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByRole('alert')).toHaveTextContent('Required');
  });
  it('password reveal toggles the input type', async () => {
    render(Input, { label: 'Password', type: 'password', revealable: true });
    const input = screen.getByLabelText('Password', { selector: 'input' });
    expect(input).toHaveAttribute('type', 'password');
    await userEvent.click(screen.getByRole('button', { name: 'Show password' }));
    expect(input).toHaveAttribute('type', 'text');
    expect(screen.getByRole('button', { name: 'Hide password' })).toHaveAttribute('aria-pressed', 'true');
  });
});

describe('Modal', () => {
  it('renders nothing when closed', () => {
    render(Modal, { open: false, title: 'Hi', onclose: () => {} });
    expect(screen.queryByRole('dialog')).toBeNull();
  });
  it('is an accessible modal dialog that closes on Escape and backdrop click', async () => {
    const onclose = vi.fn();
    render(Modal, { open: true, title: 'Rename', description: 'Pick a name', onclose, children: text('body') });
    const dlg = screen.getByRole('dialog', { name: 'Rename' });
    expect(dlg).toHaveAttribute('aria-modal', 'true');
    expect(dlg).toHaveAccessibleDescription('Pick a name');
    await fireEvent.keyDown(dlg, { key: 'Escape' });
    expect(onclose).toHaveBeenCalledTimes(1);
    await fireEvent.click(dlg.parentElement!);
    expect(onclose).toHaveBeenCalledTimes(2);
    await userEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onclose).toHaveBeenCalledTimes(3);
  });
  it('cannot be dismissed while busy', async () => {
    const onclose = vi.fn();
    render(Modal, { open: true, title: 'Saving', onclose, dismissible: false });
    await fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(onclose).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull();
  });
});

describe('Breadcrumbs', () => {
  it('renders the path and navigates', async () => {
    const onnavigate = vi.fn();
    render(Breadcrumbs, { path: 'Photos/2026/Trip', onnavigate });
    const nav = screen.getByRole('navigation', { name: 'Folder path' });
    expect(nav).toHaveTextContent('My Drive');
    expect(screen.getByText('Trip').closest('[aria-current]')).toHaveAttribute('aria-current', 'page');
    await userEvent.click(screen.getByRole('button', { name: '2026' }));
    expect(onnavigate).toHaveBeenCalledWith('Photos/2026');
    await userEvent.click(screen.getByRole('button', { name: 'My Drive' }));
    expect(onnavigate).toHaveBeenLastCalledWith('');
  });
  it('collapses long paths', async () => {
    const onnavigate = vi.fn();
    render(Breadcrumbs, { path: 'a/b/c/d/e/f', onnavigate, maxVisible: 3 });
    const more = screen.getByRole('button', { name: /hidden folders: a, b, c, d/ });
    await userEvent.click(more);
    expect(onnavigate).toHaveBeenCalledWith('a/b/c/d');
    expect(screen.queryByRole('button', { name: 'b' })).toBeNull();
  });
});

describe('ProgressBar', () => {
  it('exposes the value', () => {
    render(ProgressBar, { value: 0.426, label: 'Upload' });
    const bar = screen.getByRole('progressbar', { name: 'Upload' });
    expect(bar).toHaveAttribute('aria-valuenow', '43');
  });
  it('clamps and supports indeterminate', () => {
    render(ProgressBar, { value: 7, label: 'A' });
    expect(screen.getByRole('progressbar', { name: 'A' })).toHaveAttribute('aria-valuenow', '100');
  });
  it('indeterminate has no value', () => {
    render(ProgressBar, { label: 'B' });
    expect(screen.getByRole('progressbar', { name: 'B' })).not.toHaveAttribute('aria-valuenow');
  });
});

describe('FileRow / FileTile', () => {
  const item = { path: 'Docs/report.pdf', name: 'report.pdf', isDir: false, size: 2048, modifiedAt: new Date().toISOString() };
  const actions = [{ id: 'rename', label: 'Rename' }, { id: 'delete', label: 'Delete', danger: true }];

  it('opens on click and reports menu actions', async () => {
    const onopen = vi.fn();
    const onaction = vi.fn();
    render(FileRow, { item, actions, onopen, onaction });
    expect(screen.getByText('2.0 KB')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'report.pdf' }));
    expect(onopen).toHaveBeenCalledWith(item);

    const trigger = screen.getByRole('button', { name: 'Actions for report.pdf' });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    await userEvent.click(trigger);
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('menuitem', { name: 'Rename' })).toHaveFocus();
    await userEvent.keyboard('{ArrowDown}{Enter}');
    expect(onaction).toHaveBeenCalledWith('delete', item);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('closes the menu on Escape and returns focus', async () => {
    render(FileRow, { item, actions, onopen: () => {}, onaction: () => {} });
    const trigger = screen.getByRole('button', { name: 'Actions for report.pdf' });
    await userEvent.click(trigger);
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(trigger).toHaveFocus();
  });

  it('highlights filter matches', () => {
    const { container } = render(FileRow, { item, actions, onopen: () => {}, onaction: () => {}, highlight: 'PORT' });
    expect(container.querySelector('mark')).toHaveTextContent('port');
  });

  it('folders show their file count', () => {
    render(FileRow, { item: { ...item, name: 'Docs', path: 'Docs', isDir: true, count: 3 }, actions, onopen: () => {}, onaction: () => {} });
    expect(screen.getByText(/3 files/)).toBeInTheDocument();
  });

  it('tile shows a thumbnail when given one', () => {
    const { container } = render(FileTile, { item, actions, onopen: () => {}, onaction: () => {}, thumbnail: '/x.png' });
    expect(container.querySelector('img')).toHaveAttribute('src', '/x.png');
  });
});

describe('EmptyState', () => {
  it('renders title, message and actions', () => {
    render(EmptyState, { title: 'Nothing here', message: 'Upload something', children: text('ACTION') });
    expect(screen.getByRole('heading', { name: 'Nothing here' })).toBeInTheDocument();
    expect(screen.getByText('Upload something')).toBeInTheDocument();
    expect(screen.getByText('ACTION')).toBeInTheDocument();
  });
});

describe('Sidebar', () => {
  it('marks the active item and reports selection', async () => {
    const onselect = vi.fn();
    render(Sidebar, {
      items: [{ id: 'files', label: 'Files', icon: 'folder' }, { id: 'settings', label: 'Settings', icon: 'settings' }],
      active: 'files',
      onselect,
    });
    expect(screen.getByRole('button', { name: 'Files' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('button', { name: 'Settings' })).not.toHaveAttribute('aria-current');
    await userEvent.click(screen.getByRole('button', { name: 'Settings' }));
    expect(onselect).toHaveBeenCalledWith('settings');
  });
});

describe('Toasts', () => {
  it('push, render with the right role, dismiss', async () => {
    render(ToastHost);
    const id = toast.error('Upload failed', { timeout: 0 });
    toast.success('Saved', { timeout: 0 });
    flushSync();
    expect(screen.getByRole('alert')).toHaveTextContent('Upload failed');
    expect(screen.getByRole('status')).toHaveTextContent('Saved');
    toast.dismiss(id);
    flushSync();
    expect(screen.queryByRole('alert')).toBeNull();
  });
  it('runs an action button', async () => {
    render(ToastHost);
    const run = vi.fn();
    toast.info('Moved', { timeout: 0, action: { label: 'Open', run } });
    flushSync();
    await userEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(run).toHaveBeenCalled();
  });
});

describe('PasswordStrength', () => {
  it('announces the rating', () => {
    render(PasswordStrength, { password: 'purple tiger lamp' });
    expect(screen.getByText(/Good\./)).toBeInTheDocument();
  });
});
