# Security policy

## Reporting a vulnerability

Please **don't open a public issue** for security problems.

Report privately through GitHub: **Security → Report a vulnerability** on
this repository (private vulnerability reporting). Include:

- what an attacker can do, and the conditions (network position, account,
  configuration);
- steps to reproduce or a proof of concept;
- the version (`/api/v1/health`, or the sidebar footer).

What to expect:

| | |
|---|---|
| Acknowledgement | within 72 hours |
| First assessment | within 7 days |
| Fix for a confirmed high/critical issue | target 30 days, coordinated with you |
| Credit | in the release notes, if you'd like |

Please give us a reasonable time to ship a fix before publishing details.
Good-faith research that avoids privacy violations, data destruction and
service disruption is welcome; don't access data that isn't yours.

## Supported versions

Null Drive is pre-1.0. Security fixes go into the latest release only;
upgrade to get them (docs/INSTALL.md › Upgrading).

## Scope

In scope: the server, the web app, the desktop client, the
`nulldrive-e2ee` CLI, the Docker image and the deployment templates in this
repository. What the project does and doesn't defend against is written
down in [docs/SECURITY.md](docs/SECURITY.md); a way around something it
claims to protect is exactly what we want to hear about. Limits that
document already states (for example, an actively malicious server serving
modified JavaScript to the web app) are known.
