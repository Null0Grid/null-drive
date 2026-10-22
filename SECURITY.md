# Security

## What's protected, and how

| Data | Where it lives | Protection |
|---|---|---|
| Your password | server database | Argon2id hash (64 MiB, t=3). Sign-in has per-address rate limits and per-account lockout with backoff; optional two-factor codes (TOTP) with recovery codes |
| Sessions and device / API tokens | server database: SHA-256 hashes only. Desktop app: Windows Credential Manager (OS keychain) | A leaked database holds no usable token. Each device's token is revocable on its own, and revoking cuts its live connection at once. *Search only* tokens (for Null Search) can search and nothing else |
| Files on a standard account | server: `blobs/` (chunks) and `nulldrive.db` | Readable by the server and its admin, by design. In transit: TLS when you serve Drive over HTTPS (see [docs/INSTALL.md](docs/INSTALL.md#https-with-a-reverse-proxy)). Each account sees and references only its own files and chunks |
| Files on an end-to-end encrypted account | server | Contents **and** names encrypted on your devices before upload (XChaCha20-Poly1305 with deterministic per-account keys, Argon2id for the passphrase). The server sees only sizes, folder shape and times. Turning it on re-uploads everything encrypted and then deletes every plaintext copy, history included |
| Encryption master key | never on the server unwrapped: wrapped by your passphrase and by a recovery key. Desktop: OS keychain. Browser: the tab's memory only | Lose both the passphrase and the recovery key and the files are gone: there is no backdoor, and the admin can only reset your *login* password |
| The web app | served by your server | Strict Content Security Policy, HSTS behind HTTPS, CSRF tokens on top of SameSite cookies |
| Network exposure | port 8080 (HTTP) | Plain HTTP is meant for your LAN; put HTTPS in front for anything else (reverse proxy or tunnel). The desktop app warns about plain HTTP to internet addresses and pins self-signed certificates on first use. LAN discovery (mDNS) only announces the server's address and can be turned off (`NULLDRIVE_MDNS=off`). Until the admin account exists, whoever opens the page first can create it: finish setup before exposing the port |
| `nulldrive://` links | desktop app | Only `open` with a file id is accepted; the file comes from your own server and sync folder. A link never runs a program: executables and scripts are shown in their folder instead |
| Search indexes | server: one SQLite file per account | Each request opens only the account the token belongs to. Encrypted accounts have no index |

The full threat model (a stolen laptop, a compromised server, the network, other users, and what isn't covered) is in [docs/SECURITY.md](docs/SECURITY.md).

## Reporting a vulnerability

Please don't open a public issue. Email **volosteele55@proton.me** with what an attacker can do, the steps to reproduce, and the version (`/api/v1/health` or the web app's sidebar footer). You'll get an answer within a few days. Please allow time for a fix before publishing details.

Security fixes go into the latest release only.
