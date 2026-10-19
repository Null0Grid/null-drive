# deploy/

| Path | What |
|---|---|
| `docker-compose.yml` | Recommended install: `docker compose up -d` |
| `casaos/Apps/NullDrive/` | CasaOS / ZimaOS AppStore v2 app (compose + `x-casaos` metadata + icon) |
| `unraid/nulldrive.xml` | Unraid Community Applications template |
| `truenas/docker-compose.yaml` | TrueNAS SCALE 24.10+ "Install via YAML" |
| `truenas/catalog/nulldrive/` | Draft `app.yaml` / `ix_values.yaml` for the official TrueNAS catalog |
| `assets/` | App icon (SVG + 512 px PNG) |
| `SUBMITTING.md` | How to publish each template upstream |

The image itself is built from the repo-root `Dockerfile`. Installation guide: [`docs/INSTALL.md`](../docs/INSTALL.md).
