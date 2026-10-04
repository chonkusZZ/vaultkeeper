# Vaultkeeper

Self-hosted, file-based backup with a web manager and lightweight agents. [restic](https://restic.net)
does the heavy lifting (encryption, dedup, compression, snapshots); Vaultkeeper adds scheduling,
agents, copy jobs, automatic **restore tests**, a dashboard and email alerts.

```
            ┌────────────────────────── Manager (web UI, API, scheduler, SQLite) ─────────────────────────┐
            │  control plane only: tasks, logs, schedules, alerts — backup data never passes through it   │
            └────────▲───────────────────────────────▲────────────────────────────────▲──────────────────┘
        outbound poll│                   outbound poll│                        outbound poll│
              ┌──────┴──────┐   restic over TLS   ┌───┴──────────┐   restic copy   ┌───┴──────────┐
              │ Source agent│ ──────────────────► │ Dest. agent  │ ──────────────► │ Dest. agent 2│
              │ (restic)    │  direct, encrypted  │ REST endpoint│  direct         │ (copy target)│
              └─────────────┘   & compressed      └──────────────┘                 └──────────────┘
```

* **Agents are one static binary** (`vk-agent`). They only make *outbound* connections to the manager, so they
  work behind NAT/firewalls. restic is downloaded automatically (SHA-256 verified) if it isn't installed.
* **Source → destination is direct.** A destination agent serves the restic REST protocol over HTTPS
  (self-signed cert, pinned by source agents via the manager; per-agent token). Compression and encryption
  happen on the source before anything leaves the machine.
* **SMB/NFS**: put an agent near a share and set "network share" on the job (agent mounts it for the run), or point a
  destination agent's `--data-dir` at an already-mounted share.

## Quick start

```bash
make build                                  # → bin/vk-manager, bin/vk-agent  (or: docker build -t vaultkeeper .)

# 1. manager (prints a generated admin password on first run, or set VK_ADMIN_PASSWORD)
./bin/vk-manager --listen :8080 --data-dir ./data

# 2. open http://localhost:8080 → Infrastructure → "Deploy an agent" shows the exact command, e.g.
./vk-agent run --manager http://mgr:8080 --token <enrol-token> --roles source --name fileserver
./vk-agent run --manager http://mgr:8080 --token <enrol-token> --roles dest   --name nas \
               --data-dir /mnt/backups/vaultkeeper --advertise https://nas.lan:8765
```

**Docker / Linux:** `docker-compose.yml` runs a manager plus a source and a destination agent:

```bash
VK_ADMIN_PASSWORD=changeme VK_TOKEN=$(openssl rand -hex 16) docker compose up -d --build   # http://localhost:8080
```

(`VK_TOKEN` is the agent enrolment token; the manager reads it as `VK_ENROLL_TOKEN`.) Build the images separately with
`docker build --target manager|agent .`. Run real agents as root so they can read every file and restore ownership.
SMB/NFS source mounts need `cifs-utils` / `nfs-common` on the agent host.

`vk-agent systemd` prints a unit file; `vk-agent install-restic` fetches restic explicitly.
A machine can be `--roles source,dest`; then backups to itself skip the network entirely.
Everything also reads `VK_*` environment variables (see `--help`).

## Features

| | |
|---|---|
| Backups | restic snapshots, encrypted (AES-256), deduplicated, compression auto/max/off, include paths, exclusions, bandwidth cap |
| Scheduling | cron-based (UI presets: hourly, every N hours, daily, weekly, custom), pause per job, run-now |
| Retention | "maximum restore points" per job *and* per copy job; pruned automatically after each run, or on demand with **Prune now** (job page / copy-jobs table) |
| Copy jobs | `restic copy` between destinations, agent→agent, own schedule/retention/key |
| **Backup tests** | Scheduled restore of a *random file chosen at the first backup* (or one you pick) from the latest restore point, size + hash verified (`restic restore --verify`), followed by `restic check` (optionally reading N% of pack data). A test also runs right after the first backup. If the chosen file later vanishes, random mode picks a new one and warns. |
| Alerts | SMTP (STARTTLS / TLS / none). Choose the minimum level that emails: everything · warnings+failures · failures only · never. Also alerts when an agent goes offline/returns. |
| Dashboard | per-job last run, result, 14-run history, next run, last test, copy jobs, agent health, free destination space |
| Infrastructure | agents, roles, versions, online state, disk free, per-repository size on each destination |
| **Mirror jobs** | A *raw* file-for-file mirror (plain folder tree, not a restic repo) kept identical to the source, **deletions included**. Only new/changed files are sent (size + mtime, or content checksum); transfers are per-file atomic, run in parallel, and large files **resume mid-file** after an interruption (kill the agent, run again — it continues). Safety guards: never deletes if the source is empty/missing/partly unreadable or if more than N% of the destination would go (override per run); **Preview** shows exactly what a run would do without changing anything. Source→destination is direct agent-to-agent over TLS, or between two folders on one agent. Optional ownership + ACL/xattr preservation, exclusions, bandwidth limit, SMB/NFS source. |
| **Backup explorer** | Pick a job (primary repository or an offsite copy) and a restore point by date; the destination agent decrypts it on demand. Browse folders, search by file name, then **download** (file, or folder as .zip, streamed through the manager with no temp file) or **restore** selected items to the **original location** (on the source agent) or a **new location** on any online agent, with an overwrite policy (if changed / always / if newer / never). Works with the primary destination offline by browsing the copy. |
| Logs | searchable by level/job/text; each run has its own live log and a Cancel button |

## Mirror jobs: how they work and what to know

* Set where destination agents keep mirrors with `--mirror-root /mnt/bigdisk/mirrors` (default `<data-dir>/mirrors`); each job
  names a folder beneath it. Jobs can't write outside it, and two jobs can't overlap.
* Each run walks both trees in the same order and streams a merge-join (no full listing held in memory), so memory stays small
  even for tens of millions of files; the cost of a run is dominated by scanning both trees, like rsync. Only differences are
  copied. Extra temp space (one line per differing folder/deletion) is used on the source agent during a run.
* An interrupted run just continues next time: finished files are skipped, a half-sent file resumes from the bytes already on
  the destination (partials live outside the mirror, so the mirrored tree never contains half-written files). If an agent is
  killed, the manager fails the orphaned run immediately so you can re-run at once.
* Deletions are applied **last**, after copying, and only if the whole source scan succeeded. Recommended: run **Preview**
  first, keep the safety limit on, and remember deleted files are gone (no restore points); use a Backup job for history.
* Always preserved: contents, modification times, permission bits (incl. setuid/setgid/sticky), symlinks, empty folders.
  Not preserved: hard-link relationships (copied as separate files), sparse holes, and special files (sockets/devices).
  Timestamps are compared with 1 s tolerance so FAT/SMB destinations don't trigger endless re-copies.
* **Ownership & ACLs (optional, per job):**
  * *Owner/group* — set by numeric ID, or by user/group **name** when IDs differ between machines (unknown names fall back to
    the numeric ID). The **destination agent must run as root** to change ownership; otherwise files are still copied and the
    run ends with a warning explaining why. Not supported on Windows.
  * *ACLs and extended attributes* — on Linux, POSIX ACLs (access and default), file capabilities and `user.*` attributes;
    on macOS, extended attributes only (macOS ACLs are a separate system and are not copied). ACL entries are copied
    verbatim, so their numeric user/group IDs must mean the same on both machines. NFSv4 ACLs are only copied where the
    filesystem exposes them as `system.nfs4_acl`. `security.selinux` and `trusted.*` are deliberately not copied.
  * An ownership/ACL-only change (which doesn't alter size or mtime) is detected and applied **without re-copying data**.
    Turning these on makes the comparison scan a little slower (extra metadata reads on both sides), so leave them off
    if you don't need them.

## Managing everything from the web UI

Day-to-day operation never needs a shell on the manager. What is covered, and the few things that are deliberately not:

| Area | In the UI |
|---|---|
| Backup / copy / mirror jobs | create, edit, pause, run, preview, prune, delete — **delete can also remove the stored data** (typed-name confirmation) |
| Restore points | browse, search, download, restore (original / new location), **delete a single restore point** |
| Orphaned data | Infrastructure lists repositories no job uses, with a typed-confirmation delete |
| Agents | status, rename, remove, **Configure** (roles, listen address, advertised address, mirror folder) and **Restart**; applied by the agent itself when idle |
| Alerts | SMTP, minimum level, **low-disk-space threshold** |
| Vaultkeeper itself | **encrypted configuration export / restore**, optional **daily automatic export**, system info |
| Access | admin password |

* **Configuration backup** (Settings): one encrypted `.vkcfg` file (scrypt + AES-256-GCM) holds jobs, agents *with their
  credentials* (so enrolled agents reconnect to a rebuilt manager by themselves), settings and every repository key. Run history
  and logs are not included. Restoring **replaces** all configuration and the admin password becomes the one in the backup.
  Without the passphrase the file is useless, and repository keys are the only thing standing between you and unreadable
  backups — keep the file and passphrase somewhere other than the manager. The optional automatic export writes into a folder
  on the manager (default `<data-dir>/config-backups`, mode 0600, newest N kept); its passphrase is stored on the manager so it
  can run unattended.
* **Agent configuration** pushed from the UI is saved on the agent as `<data-dir>/agent-config.json` and *overrides* the matching
  command-line flags (roles, `--listen`, `--advertise`, `--mirror-root`). The agent restarts itself (exec, so it works with or
  without systemd/Docker supervision) once it has no tasks running. A role can't be removed while a job uses it, and changing a
  mirror folder does not move existing mirrors (you're warned and must confirm).
* **Still outside the UI by design:** the manager's own start-up settings (listen address, TLS certificate, data directory — shown
  read-only under Settings → About), installing or upgrading the agent binaries and restic, and multiple user accounts / roles.
  Repository keys and a copy job's source/destination are fixed once created.

## Security model (internal-network tool)

* UI: single admin password (bcrypt), signed `HttpOnly`/`SameSite=Strict` session cookie, login throttling.
  Serve behind TLS (`--tls-cert/--tls-key` or a reverse proxy) if the network isn't fully trusted.
* Agents enrol with a shared token and then use a per-agent secret. Data endpoints refuse all requests until the
  manager provisions a token, never allow repo/config deletion, and are write-once per blob.
* **Repository keys are stored in the manager database in plain text** (the manager must hand them to agents).
  Protect `data/vaultkeeper.db`, and use *Show key* on each job to store a copy in your password manager —
  without the key a repository cannot be restored. Backups are encrypted on the source; destinations only ever see ciphertext.

## Restoring

Use **Backup explorer** in the UI. Restores run as normal logged runs (live log, cancel, email on failure), verify file
contents after writing, and can be started from any restore point. Original-location restore isn't offered for jobs that back
up a network share or for Windows agents (use a new location). Browsing/downloads send *decrypted* data from the destination
agent through the manager to your browser, so serve the manager over TLS if the network isn't trusted.

If the manager itself is lost, restic still works directly: `RESTIC_PASSWORD=<key> restic -r <data-dir>/repos/<repo-name> restore latest --target /restore`.

## Not done / known limits

* Browser download is one file or one folder (zip) at a time; multi-select is restore-only.
* Mirror jobs are tested functionally (unit tests with the race detector, macOS agents, Linux containers) but **not at 10 TB scale** — do a first run on a subset or with Preview, and raise parallel transfers for many small files.
* Verified on Linux (arm64 containers) and macOS: backup, tests, explorer, restores, same-machine source+destination. SMB/NFS mounting and the Windows agent compile but are untested; mounting needs root on the agent.
* Single admin account; no per-user roles. SQLite, single manager instance.
* Needs restic 0.16+ (the agent warns if an older one is found in PATH). Before backups, tests, copies, prunes and restores agents clear *stale* repository locks (left by a crashed/killed agent) via `restic unlock`, which never removes a live lock.
* Missed schedules while the manager is down are not replayed.
