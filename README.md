# msx

A Microsoft Graph CLI built for both humans and agents.

The design priority is not breadth. It is **auth durability**:
- store both access and refresh tokens on disk
- rotate refresh tokens safely when Microsoft returns a new one
- preserve the existing refresh token if Microsoft does not return a replacement during refresh
- avoid partial writes and token-loss corruption
- isolate profiles cleanly across personal and org accounts
- make the CLI predictable for automation

## What it does already

### Durable auth layer
- **Device-code login** for new sign-ins
- **1Password import** for existing Microsoft account setups
- **JSON state export/import** for explicit profile backup and migration
- Stores profile metadata + access token + refresh token in a single local embedded DB
- Uses **bbolt** (transactional, crash-safe, single-writer, durable file-backed store)
- Refreshes tokens **on demand** under a write transaction so concurrent callers do not stomp each other
- Persists rotated refresh tokens atomically with the new access token
- Retries a request once with a forced token refresh if Graph returns `401 Unauthorized`
- Uses private filesystem permissions (`0700` dir, `0600` DB file)

### Useful read-only commands
- `whoami`
- `mail` with sender/date/query/folder/unread filters
- `mail-get` for one message record by id
- `agenda` with explicit time range and query filtering
- `event-get` for one calendar event by id
- `files` for OneDrive listing/search
- `file-get` for one OneDrive item by id
- `contacts` supports display-name and email-prefix matching, and `contact-get` fetches one contact in detail (may need extra Graph consent beyond the baseline app setup)
- `sites` supports SharePoint/org search, and `site-get` fetches one site record in detail (likewise needs extra consent if the profile was imported with baseline scopes only)
- `next` to continue any returned `@odata.nextLink`
- `profiles`

## Why Go

Because Python CLIs are too often a pile of polite lies about portability.

Go gives us:
- one binary
- simpler local builds
- better distribution ergonomics
- less runtime weirdness for agent-driven automation

## Install

For normal dogfooding/use, install the latest successful official GitHub Actions artifact into a local `bin/` folder:

```bash
./scripts/install-from-github-actions.sh ./bin
./bin/msx version
./bin/msx --profile personal whoami
```

The installer now:
- downloads the platform-specific packaged artifact from `package.yml`
- downloads the matching `SHA256SUMS` artifact from the same workflow run
- verifies the archive checksum before unpacking
- prints the installed binary's embedded provenance via `msx version`

For local development only, you can still build directly:

```bash
go build ./cmd/msx
# or
GO111MODULE=on go run ./cmd/msx --help
```

## Auth model

Default state location:
- `~/.config/msx/state.db`

Override with:
- `MSX_HOME=/some/path`

### Token store encryption (`MSX_STORE_KEY`)

`MSX_STORE_KEY` controls how token values are stored on disk.  Three modes
are supported:

| Value | Behaviour |
|---|---|
| *(not set)* | Plain text, **warning printed to stderr** on every invocation. |
| `unsafe-plain` | Plain text, warning suppressed (explicit acknowledgement). |
| `aes-256-gcm:<hex-key>` | AES-256-GCM encryption; key is 32 bytes as 64 hex chars. |
| `keyring` | Key stored in / retrieved from the platform keyring (see below). |

Token values are encrypted in the bbolt store using **AES-256-GCM** with a
random 12-byte nonce per write.  Profile metadata (client ID, authority,
scopes) is not encrypted.

**Consistency is enforced**: if stored tokens were written in one mode and
read in another, msx will fail with a clear error rather than silently
mis-reading data.

#### `aes-256-gcm` mode

```sh
# Generate a key (store it somewhere safe — 1Password works well):
openssl rand -hex 32

# Export it in every shell session that runs msx:
export MSX_STORE_KEY=aes-256-gcm:<64-hex-chars>
```

- The key is **never persisted to disk** by msx.  You are responsible for keeping it.
- If you lose the key you can still recover tokens using `state-export` while
  the key is available, then reinstate them with `state-import` once you have
  a replacement.
- Best for: automation, CI/CD, scripts where you control the environment.

#### `keyring` mode

```sh
export MSX_STORE_KEY=keyring
```

msx uses the platform keyring to store and retrieve the encryption key:
- **macOS** — Keychain
- **Linux** — Secret Service (GNOME Keyring / KWallet) with a file-backend
  fallback for headless environments
- **Windows** — DPAPI / Windows Credential Manager

If no key exists yet, msx generates a 32-byte random key and saves it
automatically.  The key is stored under service `msx`, item `store-key`.

- Best for: interactive developer machines where you want zero key management.
- Note: may prompt interactively on some platforms.  For automation use
  `aes-256-gcm:<hex-key>` instead.

#### `unsafe-plain` mode

```sh
export MSX_STORE_KEY=unsafe-plain
```

Tokens are stored as plain JSON.  This is the default when `MSX_STORE_KEY` is
absent, but setting it explicitly suppresses the startup warning.

- Best for: trusted single-user machines or environments where filesystem
  permissions (`0700` dir, `0600` DB file) are your only security boundary.

Stored per profile:
- authority / tenant hint
- client ID
- scope set
- account email hint
- access token
- refresh token
- token type
- expiry timestamp
- obtained timestamp
- raw token payload

Backup/export format:
- JSON with explicit `schema` and `version` markers
- one file can hold one profile or multiple profiles
- exported payload includes the full stored profile record plus access token, refresh token, and raw token payload
- designed to be inspectable, diffable, and safe to move between machines

### Why bbolt?

Because auth durability matters more than being clever.

bbolt gives us:
- transactional updates
- crash-safe commits
- no homemade temp-file dance
- proper file locking
- a single local file that is easy to back up and reason about

## Scope defaults

Default import/login scopes are intentionally conservative and match the already-proven setup:
- `openid`
- `profile`
- `offline_access`
- `User.Read`
- `Mail.ReadWrite`
- `Calendars.ReadWrite`
- `Files.ReadWrite`

That makes imported profiles work immediately against the existing Microsoft accounts.

If you want contacts or broader SharePoint access, use `--scopes` during `login` or `import-op` and complete fresh consent where needed.

## Usage

Global flags:
- `--profile <name>`: profile to use, default `default`
- `--format json|text`: default `json`

Global flags may appear before or after the subcommand.

### Import existing auth from 1Password

```bash
msx --profile personal import-op --account-item 'MS Personal'
msx whoami --profile personal
```

### Fresh login via device code

```bash
msx login --profile personal --client-id YOUR_APP_ID --authority common
```

### Version / provenance

```bash
msx version
```

Returns build metadata including the embedded version string, commit SHA, and build timestamp.

For workflow artifacts:
- tagged builds report the tag as `version`
- non-tagged main-branch builds currently report `dev` plus the exact commit SHA
- GitHub Releases are intentionally not part of the flow yet; official private-dogfood packages remain GitHub Actions artifacts for now

### Profiles

```bash
msx profiles
```

### State backup / migration

Export the currently selected profile to stdout:

```bash
msx --profile personal state-export > personal-msx-state.json
```

Export all configured profiles to a file atomically:

```bash
msx state-export --all --out ./backups/msx-state.json
```

Import from a backup file:

```bash
msx state-import --in ./backups/msx-state.json
```

If the backup contains a profile name that already exists locally, import refuses to overwrite it by default. You must opt in explicitly:

```bash
msx state-import --in ./backups/msx-state.json --overwrite
```

Notes:
- `state-export` defaults to the selected `--profile`; use `--all` to include every profile in the local store
- `state-import` accepts one-profile or multi-profile backup files
- import validates the whole backup before writing and restores profile+token state together in one DB transaction
- output/input path `-` means stdout/stdin

### Mail search

```bash
msx mail --profile personal --top 10
msx mail --profile personal --sender noreply@example.com --since 2026-03-01T00:00:00Z
msx mail --profile personal --folder inbox --unread --top 25
msx mail --profile personal --query invoice --top 20
msx mail --profile personal --subject invoice --top 50
msx mail-get --profile personal AQMkAD... --body
```

### Calendar lookup

```bash
msx agenda --profile personal --start 2026-03-28T00:00:00Z --end 2026-04-04T00:00:00Z
msx agenda --profile personal --query dentist
msx event-get --profile personal AAMkAG... --body
```

### Files / OneDrive

```bash
msx files --profile personal --top 20
msx files --profile personal --path Documents
msx files --profile personal --query passport --top 20
msx files --profile personal --path Documents --kind folders
msx file-get --profile personal 01ABCDEF234567890 --format json
```

### Contacts

```bash
msx contacts --profile personal --top 20
msx contacts --profile personal --query ali
msx contacts --profile personal --query alice@example.com
msx contact-get --profile personal AAMkAG...
```

### SharePoint / org sites

```bash
msx sites --profile hexlium --query hexlium
msx site-get --profile hexlium contoso.sharepoint.com,1234abcd-0000-1111-2222-abcdefabcdef,9876dcba-3333-4444-5555-fedcbafedcba
```

### Pagination continuation

Graph already returns `@odata.nextLink` when there is another page. msx now gives you two boring ways to continue without hand-rolling HTTP:

```bash
msx mail --profile personal --top 25
msx next --profile personal --url 'https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages?$skiptoken=...'

# or keep the original command shape if you prefer
msx mail --profile personal --next-link 'https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages?$skiptoken=...'
```

For safety, `--next-link` / `next --url` only accept `https://graph.microsoft.com/...` URLs.

## Output shape

The CLI currently returns Graph-shaped JSON rather than inventing a second schema layer.

That is deliberate:
- fewer surprise transformations
- easier debugging against Microsoft docs
- agents can pass values through without lossy remapping
- `@odata.nextLink` and similar fields remain available when Graph returns them

`--format text` is now command-specific for the highest-value human read paths:
- `version`
- `profiles`
- `whoami`
- `mail`
- `agenda`
- `files`

Everything else still falls back to pretty JSON instead of inventing a lossy generic schema.

## Agent ergonomics

This CLI is meant to be scriptable and composable.

Current choices made for agent use:
- JSON output by default
- stable subcommands and flags
- explicit range flags for agenda
- direct detail-fetch commands for messages/events/files/contacts/sites
- next-page continuation via raw `@odata.nextLink` instead of inventing opaque cursors
- query flags rather than interactive prompts
- profile selection is explicit and cheap
- read-only operations for safe automation
- globals can appear before or after subcommands
- basic input validation for common footguns (`--top > 0`, valid RFC3339 timestamps, `--end` after `--start`, constrained enums like `--kind`, restricted next-link host/scheme)
- client-side narrowing where it improves ergonomics without hiding Graph data (`mail --subject`, `files --kind`)

## Safety

Current commands are **read-only** against Microsoft Graph.

This project intentionally does **not**:
- send mail
- delete content
- mutate calendar/mail/files/contacts

That keeps testing safe while the auth layer gets battle-hardened first.

## Tests

```bash
go test ./...
```

Covered areas now include:
- token JSON parsing and refresh-token preservation
- store durability/serialization behavior
- forced refresh persistence
- Graph `401` retry behavior and search headers
- CLI global flag parsing, help-path behavior, next-link validation, and filtering helpers for mail/events/files
- integration-style command tests for detail fetch (mail/contacts/sites) and next-page continuation against a stub Graph server
- output-shape checks that preserve top-level Graph fields like `@odata.nextLink` while applying client-side filters

## CI and packaging

GitHub Actions now provides two lanes:
- `ci.yml` runs `gofmt -l .`, `go vet ./...`, `go test ./...`, `go build ./cmd/msx`, and `go test -race ./...` with matrix coverage on Ubuntu, macOS, and Windows
- `package.yml` builds official archives for Linux/macOS/Windows, stamps the binary with version/commit/build-date metadata, uploads per-platform artifacts, and publishes a `SHA256SUMS` artifact for verification

Local dogfooding should prefer those packaged artifacts over ad-hoc local builds.

## Verified non-destructive flows

Tested against the existing configured accounts:
- `MS Personal`: import, `whoami`, `mail`, `agenda`, `files`, mail search, file search
- `MS Hexlium`: import, `whoami`, `mail`, `agenda`

## Project tracking

Backlog and remaining work live in GitHub issues, not in this README.
Treat the issue tracker as the source of truth for what is left: https://github.com/corruptmem/msx/issues
