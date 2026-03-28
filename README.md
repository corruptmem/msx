# msx

A Microsoft Graph CLI built for both humans and agents.

The design priority is not breadth. It is **auth durability**:
- store both access and refresh tokens on disk
- rotate refresh tokens safely when Microsoft returns a new one
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
- Uses private filesystem permissions (`0700` dir, `0600` DB file)

### Useful read-only commands
- `whoami`
- `mail` with sender/date/query filters
- `agenda` with explicit time range and query filtering
- `files` for OneDrive listing/search
- `contacts` command is present but needs extra Graph consent beyond the current baseline app setup
- `sites` command is present for SharePoint/org search, but likewise needs extra consent if the profile was imported with baseline scopes only
- `profiles`

## Why Go

Because Python CLIs are too often a pile of polite lies about portability.

Go gives us:
- one binary
- simpler local builds
- better distribution ergonomics
- less runtime weirdness for agent-driven automation

## Install

```bash
go build ./cmd/msx
```

Or install to your Go bin:

```bash
go install ./cmd/msx
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
msx mail --profile personal --query invoice --top 20
```

### Calendar lookup

```bash
msx agenda --profile personal --start 2026-03-28T00:00:00Z --end 2026-04-04T00:00:00Z
msx agenda --profile personal --query dentist
```

### Files / OneDrive

```bash
msx files --profile personal --top 20
msx files --profile personal --path Documents
msx files --profile personal --query passport --top 20
```

### Contacts

```bash
msx contacts --profile personal --top 20
msx contacts --profile personal --query ali
```

### SharePoint / org sites

```bash
msx sites --profile hexlium --query hexlium
```

## Agent ergonomics

This CLI is meant to be scriptable and composable.

Current choices made for agent use:
- JSON output by default
- stable subcommands and flags
- explicit range flags for agenda
- query flags rather than interactive prompts
- profile selection is explicit and cheap
- read-only operations for safe automation
- globals can appear before or after subcommands

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

## Verified non-destructive flows

Tested against the existing configured accounts:
- `MS Personal`: import, `whoami`, `mail`, `agenda`, `files`, mail search, file search
- `MS Hexlium`: import, `whoami`, `mail`, `agenda`

## What still remains to make it excellent

High-value next steps:
- richer mail filtering (`subject`, pagination/cursors, folder discovery)
- contact lookup by email and broader consent-aware search
- file metadata normalization for cleaner downstream indexing
- message/event/body detail fetch commands
- OS keychain / age-backed optional encryption-at-rest for the DB
- better documented output schemas per command
- backup/export/import tooling for state migration
