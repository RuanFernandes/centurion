# Project storage

Centurion keeps project identity separate from runtime state.

## Manifest

The first workspace folder of a project contains:

```text
.centurion/project.json
```

The manifest stores the project ID, display name, description, and the complete list of workspace folders. Additional folders can point to other repositories or services needed by the same workflow.

## Local state

SQLite, logs, temporary runtime data, and Codex execution history stay in the per-user application data directory:

```text
%LOCALAPPDATA%\Centurion\centurion.db
```

This prevents agent conversations, approvals, and machine-specific state from being copied into a source repository. The generated `.centurion/.gitignore` ignores local state while allowing the manifest to be versioned.

## Snapshots

The Projects view can export a JSON snapshot to:

```text
.centurion\exports\snapshot-<timestamp>.json
```

Snapshots contain the project catalog, agent profiles, workflows, schedules, and history metadata. They are intended for review, backup, and migration—not as the live database.

## Safety

All workspace folders are normalized before persistence. Paths outside the selected workspace roots are rejected by the security layer. Secrets are redacted from audit metadata and should never be placed in project manifests, workflow definitions, prompts, or snapshots.
