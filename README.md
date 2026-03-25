<div align="center">

# SealLayer Backend

**Stateless HTTP API** that accepts content **SHA-256 hashes**, batches them, appends rows to a **public Git ledger**, and returns **SIP v1–style signed receipts** (OpenPGP detached signature over the batch root).

[Features](#features) · [Quick start](#quick-start) · [Configuration](#configuration) · [API](#api) · [Deploy (Coolify)](#deploy-with-coolify) · [After deploy](#after-deployment-checklist)

</div>

---

## Features

| | |
|---|---|
| **No database** | In-memory queue + batch status (lost on restart). |
| **Git as ledger** | Clone → commit (signed optional) → push to your GitHub repo. |
| **GPG** | Detached signature on batch root; optional GPG-signed Git commits. |
| **Observability** | Structured logs to **stdout** only (no log files). |

---

## Quick start

### Requirements

- **Go** 1.22+ (see `go.mod`)
- **Git**, **GnuPG** (`gpg`) on the host or in the container
- A **GitHub** repo the token can push to
- **Optional:** Docker (for Coolify / container deploy)

### Clone & run

```bash
cd backend
cp .env.example .env   # if you add an example file; otherwise set env vars directly
go run ./cmd/server
```

Default listen address: `:8080` (`HTTP_LISTEN_ADDR`).

### Health check

```bash
curl -s http://localhost:8080/healthz
```

---

## Configuration

Set variables in your environment (or Coolify “Environment” UI). **Never commit** secrets.

### Required

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | PAT or token with `repo` scope (push to ledger repo). |
| `GITHUB_OWNER` | GitHub org or user owning the repo. |
| `GITHUB_REPO` | Repository name (the ledger). |
| `GIT_AUTHOR_NAME` | Git commit author name (your org/bot identity). |
| `GIT_AUTHOR_EMAIL` | Git commit author email (**must match a GitHub-verified email** for “Verified” badges). |
| `GPG_KEYID` | Secret key ID or fingerprint for signing. |
| `GPG_PASSPHRASE` | Passphrase for the secret key (also used when importing an encrypted key at startup). |
| `GPG_PUBLIC_KEY_FINGERPRINT` | Shown in receipts for client-side pinning / verification. |

### Signing & optional GPG behavior

| Variable | Description |
|----------|-------------|
| `GPG_PRIVATE_KEY_ARMORED` | *(Docker / CI)* Full ASCII-armored secret key. On startup the app runs `gpg --import` into `GNUPGHOME` before serving. |
| `GPG_PRIVATE_KEY_FILE` | Path to a key file (e.g. `/run/secrets/gpg.asc`). If set, it overrides `GPG_PRIVATE_KEY_ARMORED` for import. |
| `GPG_PROGRAM` | *(Often Windows)* Full path to `gpg` so Git uses the same binary/keyring as signing (e.g. `C:\Program Files\GnuPG\bin\gpg.exe`). |
| `GIT_SIGN_COMMITS` | `true` (default): GPG-sign Git commits. `false`: unsigned commits; detached receipt signature still produced. |

The Docker image sets `GNUPGHOME=/app/.gnupg` so the imported key lives in a predictable place inside the container.

### Optional tuning

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_LISTEN_ADDR` | `:8080` | Bind address. |
| `GITHUB_BRANCH` | `main` | Target branch. |
| `GITHUB_REPO_URL` | auto | Override clone URL if needed. |
| `BATCH_INTERVAL` | `10s` | Worker tick / batching window. |
| `REQUEST_PENDING_MAX` | `30s` | How long `POST /v1/seal` waits for inline completion before `202`. |
| `QUEUE_CAPACITY` | `1000` | Max queued jobs. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `GIN_MODE` | release | Set to `debug` only for Gin route debug noise. |

### Reserved / future env

See [`docs/RESERVED.md`](docs/RESERVED.md) for `SEAL_PER_IP_PER_MINUTE`, `LEDGER_RAW_BASE_URL`, and unused GitHub API helpers.

### GPG private key in Docker (Coolify / `private.asc`)

You do **not** copy `private.asc` into the image. Pick one:

1. **Secret file mount** — store the armored key as a Coolify/Docker secret file and set  
   `GPG_PRIVATE_KEY_FILE=/run/secrets/your_key.asc` (path depends on your platform).
2. **Multiline env** — paste the full `-----BEGIN PGP PRIVATE KEY BLOCK-----` block into  
   `GPG_PRIVATE_KEY_ARMORED` (supported by many hosts as a “secret” variable).

On startup the server imports the key into `GNUPGHOME` (`/app/.gnupg` in the Dockerfile) using `GPG_PASSPHRASE` if the key is encrypted. If the key is already in the image volume (advanced), you can omit both envs.

---

## API

OpenAPI: [`openapi.yaml`](openapi.yaml).

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/healthz` | Liveness. |
| `POST` | `/v1/seal` | Body: `{ "content_hash": "<64 hex lowercase sha256>" }`. |
| `GET` | `/v1/status/{batch_id}` | Batch state from **server memory** (`queued`, `batching`, `pushed`, `failed`, …). |

### Example: seal

```bash
curl -s -X POST http://localhost:8080/v1/seal \
  -H "Content-Type: application/json" \
  -d '{"content_hash":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}'
```

- **`200`**: Receipt JSON in `receipt`.
- **`202`**: Processing; poll `status_url` / `GET /v1/status/{batch_id}`.
- **`503`**: Queue full.

Verification of signatures is **client-side**; the API does not need a separate verify endpoint for that model.

---

## Docker

```bash
docker build -t seallayer-backend .
docker run --rm -p 8080:8080 \
  -e GITHUB_TOKEN=... \
  -e GITHUB_OWNER=... \
  -e GITHUB_REPO=... \
  -e GPG_KEYID=... \
  -e GPG_PASSPHRASE=... \
  -e GPG_PUBLIC_KEY_FINGERPRINT=... \
  seallayer-backend
```

Image includes `git` and `gnupg` (see `Dockerfile`). Mount or inject secret keys as required by your platform; for production, prefer a keyring or secret store over plain env files where possible.

---

## Deploy with Coolify

1. **Push this repository** (or only the `backend/` tree) to GitHub.
2. In **Coolify**, create an application from the GitHub repo:
   - Build type: **Dockerfile** (path `backend/Dockerfile` if the repo is monorepo).
   - Set **port** to `8080` (or whatever you expose; map `HTTP_LISTEN_ADDR` accordingly, e.g. `:8080`).
3. Add **environment variables** in Coolify (all required + optional from [Configuration](#configuration)).
4. **Secrets:** paste `GITHUB_TOKEN`, `GPG_PASSPHRASE`, etc. only in Coolify env UI, not in the repo.
5. Deploy. Coolify will build and run the container.

---

## After deployment checklist

- [ ] **HTTPS** in front of the app (Coolify / reverse proxy TLS).
- [ ] **Firewall** only exposes necessary ports.
- [ ] **Env vars** set for production; `LOG_LEVEL` is `info` or `warn` unless debugging.
- [ ] **`GPG_PROGRAM`** on Linux container: usually `gpg` on `PATH`; if multiple GPG installs, set explicitly.
- [ ] **GitHub**: token has push access; branch protection rules allow the bot to push if required.
- [ ] **GPG**: secret key available inside the container (import at image build is **not** recommended for keys; use runtime secret injection or mounted keyring as per your security policy).
- [ ] **`GET /v1/status`**: remember status is **in-memory**; plan for restarts (clients can still verify on-chain ledger via GitHub).

---

## License

See [`LICENSE`](LICENSE) in the repository.
