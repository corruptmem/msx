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
- `contacts` supports display-name and email-prefix matching, but may need extra Graph consent beyond the baseline app setup
- `sites` command is present for SharePoint/org search, but likewise needs extra consent if the profile was imported with baseline scopes only
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

For normal dogfooding/use, install the latest official GitHub Actions artifact into a local `bin/` folder:

```bash
./scripts/install-from-github-actions.sh ./bin
./bin/msx --profile personal whoami
```

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

### Profiles

```bash
msx profiles
```

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
```

### SharePoint / org sites

```bash
msx sites --profile hexlium --query hexlium
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

`--format text` currently prints pretty JSON rather than a bespoke table renderer. That is honest, boring, and easy to diff. If a real text renderer is added later, it should be command-specific and tested.

## Agent ergonomics

This CLI is meant to be scriptable and composable.

Current choices made for agent use:
- JSON output by default
- stable subcommands and flags
- explicit range flags for agenda
- direct detail-fetch commands for messages/events/files
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
- integration-style command tests for detail fetch and next-page continuation against a stub Graph server
- output-shape checks that preserve top-level Graph fields like `@odata.nextLink` while applying client-side filters

## CI and packaging

GitHub Actions now provides two lanes:
- `ci.yml` runs `gofmt -l .`, `go vet ./...`, `go test ./...`, `go build ./cmd/msx`, and `go test -race ./...` with matrix coverage on Ubuntu, macOS, and Windows
- `package.yml` builds official archives for Linux/macOS/Windows, uploads per-platform artifacts, and publishes a `SHA256SUMS` artifact for verification

Local dogfooding should prefer those packaged artifacts over ad-hoc local builds.

## Verified non-destructive flows

Tested against the existing configured accounts:
- `MS Personal`: import, `whoami`, `mail`, `agenda`, `files`, mail search, file search
- `MS Hexlium`: import, `whoami`, `mail`, `agenda`

## Project tracking

Backlog and remaining work live in GitHub issues, not in this README.
Treat the issue tracker as the source of truth for what is left: https://github.com/corruptmem/msx/issues
