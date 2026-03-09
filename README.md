# MailM

**Migrate aliased emails out of one IMAP account into another.**

When an email address was an alias that delivered into a shared mailbox, and that address later becomes its own dedicated mailbox, MailM finds every message in the old mailbox that was originally addressed to the alias and copies it into the new one - preserving folder structure, flags, and timestamps.

**Example scenario**

| Before | After |
|--------|-------|
| `dmarc.reports@example.com` was an alias, delivered to `user@example.com` | `dmarc.reports@example.com` is now a real mailbox |
| All those messages live in `user@example.com` | They should live in `dmarc.reports@example.com` |

MailM does exactly that move, safely and repeatably.

---

## Features

- Filters by `To`, `Cc`, `Delivered-To`, `X-Original-To`, `Envelope-To`, and `X-Forwarded-To` headers
- Preserves `INTERNALDATE`, message flags (`\Seen`, `\Answered`, `\Flagged`, etc.)
- **Dry-run by default** - shows what would happen without touching anything
- **Idempotent** - a SQLite state file tracks completed copies; re-runs skip already-migrated messages
- Flexible folder mapping - scan specific folders, rename them on the way, or skip them entirely
- Date-range filtering (`--since`, `--before`)
- Optional source deletion after a successful copy (`--delete-source`)
- `--overwrite` to force re-copy messages already in the state DB
- TLS/SSL and STARTTLS support with explicit mode control
- Configurable via YAML file, CLI flags, or both (flags override config)

---

## Installation

Requires Go 1.22+.

```bash
git clone https://github.com/yourorg/mailm
cd mailm
go build -o mailm .
```

---

## Quick Start

**1. Dry run - see what would be moved**

```bash
mailm \
  --alias dmarc.reports@example.com \
  --source-host mail.example.com --source-user user@example.com --source-password "$SRC_PASS" \
  --dest-host mail.example.com   --dest-user dmarc.reports@example.com --dest-password "$DST_PASS" \
  --dry-run --dry-run-report matched.csv
```

Review the console output and `matched.csv`. No changes are made.

**2. Live run**

```bash
mailm \
  --alias dmarc.reports@example.com \
  --source-host mail.example.com --source-user user@example.com --source-password "$SRC_PASS" \
  --dest-host mail.example.com   --dest-user dmarc.reports@example.com --dest-password "$DST_PASS" \
  --no-dry-run
```

**3. Live run with source cleanup**

Add `--delete-source` to expunge matched messages from the source mailbox after each successful copy.

```bash
mailm mailm.yaml --no-dry-run --delete-source
```

---

## Config File

For anything beyond a one-liner, use a YAML config file. Copy `mailm.example.yaml` and fill it in:

```bash
cp mailm.example.yaml mailm.yaml
$EDITOR mailm.yaml
mailm mailm.yaml
```

The config file can be passed as a positional argument or via `--config`. CLI flags always override config file values.

Passwords support environment variable references - `$VAR` or `${VAR}` - so you never need to put credentials in the file:

```yaml
source:
  password: $SOURCE_IMAP_PASS
dest:
  password: $DEST_IMAP_PASS
```

---

## All CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config <path>` | | YAML config file (alternative to positional arg) |
| `--alias <addr>` | | Alias address to filter on **(required)** |
| `--source-host <host>` | | IMAP host for the source mailbox **(required)** |
| `--source-port <n>` | `993` | IMAP port for source |
| `--source-user <user>` | | Login user for source **(required)** |
| `--source-password <pass>` | | Password for source (or `$ENV_VAR`) **(required)** |
| `--source-tls <mode>` | auto | TLS mode: `ssl`, `starttls`, or `none` |
| `--source-folder <pattern>` | all | IMAP LIST pattern to scan (repeatable) |
| `--dest-host <host>` | | IMAP host for the destination mailbox **(required)** |
| `--dest-port <n>` | `993` | IMAP port for destination |
| `--dest-user <user>` | | Login user for destination **(required)** |
| `--dest-password <pass>` | | Password for destination (or `$ENV_VAR`) **(required)** |
| `--dest-tls <mode>` | auto | TLS mode: `ssl`, `starttls`, or `none` |
| `--dest-folder <folder>` | | Force all messages into one folder (overrides `folder_map`) |
| `--dry-run` | | Enable dry-run (safe; nothing is written) |
| `--no-dry-run` | | Disable dry-run and actually copy messages |
| `--delete-source` | `false` | Expunge matched messages from source after copy |
| `--overwrite` | `false` | Re-copy messages already in the state DB |
| `--since <YYYY-MM-DD>` | | Only migrate messages on or after this date |
| `--before <YYYY-MM-DD>` | | Only migrate messages before this date |
| `--state-file <path>` | `migration_state.db` | SQLite state file for idempotency tracking |
| `--log-file <path>` | stdout | Structured JSON log output |
| `--batch-size <n>` | `50` | UIDs fetched per IMAP round-trip |
| `--retry-count <n>` | `3` | Retries on transient network errors |
| `--dry-run-report <path>` | | Write a CSV of matched messages (dry-run only) |
| `--allow-insecure` | `false` | Skip TLS cert verification / permit `tls: none` |

---

## Config File Reference

```yaml
alias: dmarc.reports@example.com

source:
  host: mail.example.com
  port: 993
  tls: ssl                    # ssl | starttls | none (auto-detected from port if omitted)
  user: user@example.com
  password: $SOURCE_IMAP_PASS
  folders:                    # IMAP LIST patterns; omit to scan everything
    - INBOX
    - INBOX/*

dest:
  host: mail.example.com
  port: 993
  tls: ssl
  user: dmarc.reports@example.com
  password: $DEST_IMAP_PASS
  default_folder: INBOX       # destination for folders not in folder_map
  auto_create_folders: true   # create destination folders that don't exist
  folder_map:
    INBOX: INBOX
    INBOX/Archive: INBOX/Archive
    INBOX/Junk: null          # null = skip this source folder entirely

dry_run: true
dry_run_report: ./matched.csv # CSV of what would be copied; omit to skip
delete_source: false
overwrite: false

batch_size: 50
retry_count: 3
state_file: ./migration_state.db
log_file: ./migration.log     # omit or leave empty for stdout

# since: "2023-01-01"
# before: "2025-01-01"

allow_insecure: false
```

---

## TLS Modes

| Mode | When to use |
|------|-------------|
| `ssl` | Direct TLS connection (IMAPS). Default for port 993. |
| `starttls` | Plain connection upgraded via STARTTLS. Default for port 143. STARTTLS is **required** - MailM will not fall back to plaintext if the server doesn't advertise it. |
| `none` | No encryption. Only allowed when `allow_insecure: true`. Never use in production. |

If `tls` is omitted, MailM defaults to `ssl` on port 993 and `starttls` on all other ports.

---

## How It Works

1. MailM connects to the source mailbox and lists all folders matching `source.folders`.
2. For each folder it runs an IMAP `UID SEARCH` (optionally bounded by `since`/`before`).
3. Headers are fetched in batches and any message with the alias in `To`, `Cc`, `Delivered-To`, `X-Original-To`, `Envelope-To`, or `X-Forwarded-To` is selected.
4. Messages already recorded in the state DB are skipped (unless `--overwrite` is set).
5. In a live run, each matched message is fetched in full and `APPEND`ed to the mapped destination folder, preserving flags and `INTERNALDATE`.
6. The copy is recorded in `migration_state.db`. If the run is interrupted it can be safely restarted - already-copied messages will be skipped.
7. If `--delete-source` is set, matched messages are marked `\Deleted` and `EXPUNGE`d from the source only after all copies in that folder succeed.

---

## Recommended Workflow

```
1.  dry run          →  review console output and matched.csv
2.  live run         →  messages copied, state DB written
3.  verify           →  spot-check the destination mailbox
4.  delete-source    →  re-run with --delete-source to clean up the source
                        (state DB prevents double-deletion)
```

If anything goes wrong between steps, just re-run - MailM will pick up where it left off.
