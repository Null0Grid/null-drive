import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { ApiError } from '../../lib/api/client';
import type { KeyBundle } from '../../lib/api/types';

// Key creation is mocked: Argon2 over 64 MiB is covered by the crypto
// tests; here only the flow matters.
const fakeKeys = { authKey: 'a'.repeat(64), fingerprint: '0123456789abcdef' };
const fakeBundle = { version: 1, fingerprint: '0123456789abcdef' } as unknown as KeyBundle;
vi.mock('../../lib/crypto/keys', async (orig) => ({
  ...(await orig<typeof import('../../lib/crypto/keys')>()),
  createKeys: vi.fn(async () => ({ bundle: fakeBundle, keys: fakeKeys, recoveryKey: 'ABCD-EFGH-JKMN-PQRS-TVWX-YZ01-2345-6789' })),
}));

import Login from '../pages/Login.svelte';
import Unlock from '../pages/Unlock.svelte';
import EncryptionSetup from '../components/EncryptionSetup.svelte';
import TwoFactorCard from '../components/TwoFactorCard.svelte';
import AuditCard from '../components/AuditCard.svelte';
import { client, drive } from '../api';
import { session } from '../session.svelte';
import { vault } from '../vault.svelte';
import { WrongKeyError } from '../../lib/crypto/keys';

const user = { id: 1, username: 'alice', is_admin: true, created_at: '', totp_enabled: false, e2ee: 'off' as const };

beforeEach(() => {
  session.user = { ...user };
  session.status = 'authed';
});
afterEach(() => {
  vi.restoreAllMocks();
  vault.lock();
});

describe('Login with two-factor authentication', () => {
  it('asks for the code after the password, then signs in', async () => {
    session.status = 'anon';
    const login = vi
      .spyOn(client, 'login')
      .mockRejectedValueOnce(new ApiError(401, 'totp_required', 'enter the code'))
      .mockRejectedValueOnce(new ApiError(401, 'invalid_totp', 'bad code'))
      .mockResolvedValueOnce({ token: 't', expires_at: '', user: { ...user, totp_enabled: true } });
    render(Login, { next: '/files' });
    await userEvent.type(screen.getByLabelText('Username'), 'alice');
    await userEvent.type(screen.getByLabelText('Password'), 'hunter2hunter2');
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('Two-factor authentication')).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Code'), '000000');
    await userEvent.click(screen.getByRole('button', { name: 'Verify' }));
    expect(await screen.findByText(/That code isn't valid/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Code'), '123456');
    await userEvent.click(screen.getByRole('button', { name: 'Verify' }));
    expect(login).toHaveBeenLastCalledWith('alice', 'hunter2hunter2', '123456');
    expect(session.status).toBe('authed');
  });

  it('shows the lockout message from the server', async () => {
    session.status = 'anon';
    vi.spyOn(client, 'login').mockRejectedValue(new ApiError(429, 'locked_out', 'too many failed sign-ins for this account; try again in 30 seconds'));
    render(Login, { next: '/files' });
    await userEvent.type(screen.getByLabelText('Username'), 'alice');
    await userEvent.type(screen.getByLabelText('Password'), 'x');
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText(/Too many failed sign-ins.*30 seconds/)).toBeInTheDocument();
  });
});

describe('Unlock screen', () => {
  it('reports a wrong passphrase and unlocks with the right one', async () => {
    session.user = { ...user, e2ee: 'on' };
    const unlock = vi
      .spyOn(vault, 'unlock')
      .mockRejectedValueOnce(new WrongKeyError())
      .mockImplementationOnce(async () => {
        vault.unlocked = true;
        return fakeKeys as never;
      });
    render(Unlock);
    await userEvent.type(screen.getByLabelText('Encryption passphrase'), 'wrong one!!');
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText("That passphrase doesn't match.")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Encryption passphrase'), 'right passphrase');
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(unlock).toHaveBeenLastCalledWith('right passphrase', false);
    expect(vault.unlocked).toBe(true);
  });

  it('switches to the recovery key and then offers a new passphrase', async () => {
    session.user = { ...user, e2ee: 'on' };
    const open = vi.spyOn(vault, 'open').mockResolvedValue(fakeKeys as never);
    render(Unlock);
    await userEvent.click(screen.getByRole('button', { name: /Use the recovery key/ }));
    await userEvent.type(screen.getByLabelText('Recovery key'), 'ABCD-EFGH-JKMN-PQRS-TVWX-YZ01-2345-6789');
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(open).toHaveBeenCalledWith('ABCD-EFGH-JKMN-PQRS-TVWX-YZ01-2345-6789', true);
    expect(await screen.findByText('Set a new passphrase')).toBeInTheDocument();
    expect(vault.unlocked).toBe(false); // the app stays closed until a choice is made
    await userEvent.click(screen.getByRole('button', { name: 'Skip' }));
    expect(vault.unlocked).toBe(true);
    expect(drive.keys).toBe(fakeKeys);
  });
});

describe('Turning on encryption', () => {
  it('requires the recovery key to be confirmed before anything is sent', async () => {
    const enable = vi.spyOn(client, 'e2eeEnable').mockResolvedValue({ state: 'on', plaintext_files: 0, fingerprint: '0123456789abcdef' });
    vi.spyOn(client, 'e2eeStatus').mockResolvedValue({ state: 'on', plaintext_files: 0 });
    const ondone = vi.fn();
    render(EncryptionSetup, { ondone });
    await userEvent.type(screen.getByLabelText('Encryption passphrase'), 'correct horse battery');
    await userEvent.type(screen.getByLabelText('Repeat it'), 'correct horse battery');
    await userEvent.click(screen.getByRole('button', { name: 'Create keys' }));
    // Not yet: the "I understand" box isn't ticked.
    expect(screen.queryByTestId('recovery-key')).toBeNull();
    await userEvent.click(screen.getByRole('checkbox'));
    await userEvent.click(screen.getByRole('button', { name: 'Create keys' }));
    expect(await screen.findByTestId('recovery-key')).toHaveTextContent('ABCD-EFGH-JKMN-PQRS-TVWX-YZ01-2345-6789');

    const turnOn = screen.getByRole('button', { name: 'Turn on encryption' });
    expect(turnOn).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/Type the last group/), '6789');
    expect(turnOn).toBeDisabled(); // still needs "I've saved it"
    await userEvent.click(screen.getByRole('checkbox', { name: /saved my recovery key/ }));
    expect(enable).not.toHaveBeenCalled();
    await userEvent.click(turnOn);
    expect(enable).toHaveBeenCalledWith(fakeBundle, 'a'.repeat(64));
    expect(await screen.findByText('End-to-end encryption is on.')).toBeInTheDocument();
    expect(drive.keys).toBe(fakeKeys);
    expect(session.user?.e2ee).toBe('on');
    await userEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(ondone).toHaveBeenCalled();
  });

  it('rejects a short passphrase', async () => {
    render(EncryptionSetup, {});
    await userEvent.type(screen.getByLabelText('Encryption passphrase'), 'short');
    await userEvent.type(screen.getByLabelText('Repeat it'), 'short');
    await userEvent.click(screen.getByRole('checkbox'));
    await userEvent.click(screen.getByRole('button', { name: 'Create keys' }));
    expect(await screen.findByText(/at least 10 characters/i)).toBeInTheDocument();
  });
});

describe('Two-factor setup', () => {
  it('shows the QR code and secret, confirms a code, then shows recovery codes', async () => {
    vi.spyOn(client, 'totpSetup').mockResolvedValue({ secret: 'JBSWY3DPEHPK3PXP', uri: 'otpauth://totp/Null%20Drive:alice?secret=JBSWY3DPEHPK3PXP' });
    const enable = vi.spyOn(client, 'totpEnable').mockResolvedValue({ recovery_codes: ['aaaaa-bbbbb', 'ccccc-ddddd'] });
    render(TwoFactorCard);
    await userEvent.click(screen.getByRole('button', { name: 'Set up…' }));
    expect(await screen.findByTestId('totp-secret')).toHaveTextContent('JBSW Y3DP EHPK 3PXP');
    expect(screen.getByRole('img', { name: /QR code/ })).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Code from the app'), '123 456');
    await userEvent.click(screen.getByRole('button', { name: 'Turn on' }));
    expect(enable).toHaveBeenCalledWith('123456');
    expect(await screen.findByTestId('totp-recovery-codes')).toHaveTextContent('aaaaa-bbbbb');
    expect(session.user?.totp_enabled).toBe(true);
  });
});

describe('Security log', () => {
  it('labels events and pages', async () => {
    const audit = vi
      .spyOn(client, 'audit')
      .mockResolvedValueOnce({
        events: [
          { id: 3, at: new Date().toISOString(), event: 'login_failed', username: 'alice', ip: '10.0.0.9', detail: { reason: 'password' } },
          { id: 2, at: new Date().toISOString(), event: 'device_added', username: 'alice', detail: { device: 'Laptop', via: 'pairing' } },
        ],
        next: 2,
      })
      .mockResolvedValueOnce({ events: [{ id: 1, at: new Date().toISOString(), event: 'setup_completed', username: 'alice' }] });
    render(AuditCard);
    expect(await screen.findByText('Sign-in failed')).toBeInTheDocument();
    expect(screen.getByText(/wrong password · 10\.0\.0\.9/)).toBeInTheDocument();
    expect(screen.getByText(/Laptop · via pairing/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }));
    expect(await screen.findByText('Server set up')).toBeInTheDocument();
    expect(audit).toHaveBeenLastCalledWith(2, 25);
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull();
  });
});
