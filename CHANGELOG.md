# Changelog

All notable changes to Centurion are documented here.

The project is currently in early alpha. Changes may include UI, schema, and
workflow contract adjustments before the first stable release.

## Unreleased

- Added project-local `.centurion/project.json` manifests and private snapshot exports.
- Added persisted estimated prompt-token budgets to workflow runs.
- Added a shared Codex App Server session status view and a bounded logical session manager.
- Added redacted security audit entries for approvals, terminal commands, project changes, runs, and exports.
- Added a fake Codex App Server integration test fixture.
- Removed the need for a separate Centurion login in the MVP; authentication remains managed by Codex CLI.
- Added Windows release packaging, CodeQL analysis, Dependabot, and repository ownership metadata.

## 0.1.0-alpha

- Initial public open-source release.
