# Reserved / unused code (intentionally kept)

These items are loaded from environment or exist in the codebase but are **not wired into runtime logic** yet. They may be used for future features—do not remove without a deliberate decision.

| Item | Location | Purpose (planned or optional) |
|------|----------|--------------------------------|
| `SEAL_PER_IP_PER_MINUTE` / `SealPerIpPerMinute` | `internal/config` | Rate limiting per client IP on `POST /v1/seal`. |
| `LEDGER_RAW_BASE_URL` / `LedgerRepoBaseRawURL` | `internal/config` | Default raw GitHub URL for the ledger; not read by HTTP handlers (e.g. future client links or verification helpers). |
| `githubclient.UpsertFile` | `internal/githubclient` | GitHub Contents API helper; current flow uses **git clone / commit / push** in `gitops`, not direct file API updates. |

**Note:** `GPG_PRIVATE_KEY_ARMORED` / `GPG_PRIVATE_KEY_FILE` are now used at startup to `gpg --import` when set.

No dead code was deleted; this file documents scope for operators and contributors.
