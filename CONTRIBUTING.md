# Contributing to Centurion

Thanks for helping improve Centurion. The project is in an early alpha, so focused changes with tests and clear scope are especially valuable.

## Before you start

1. Search existing issues and discussions.
2. Open an issue for substantial behavior or architecture changes.
3. Keep pull requests focused and explain user-visible impact.
4. Never include ChatGPT credentials, MCP secrets, local databases, workspace data, or generated build output.

## Local setup

Requirements are listed in the README. From the repository root:

    npm --prefix frontend ci
    go test ./...
    npm --prefix frontend run build

Use wails3 dev for the desktop development loop and wails3 build to verify a Windows production build.

## Change guidelines

- Prefer small, composable Go packages with explicit boundaries.
- Keep workflow conditions declarative; never add arbitrary code execution to the graph evaluator.
- Preserve approval, workspace, redaction, and rate-limit safeguards.
- Treat persisted schema changes as migrations.
- Keep generated Wails bindings in sync by using the Wails build/generation flow.
- For UI changes, include keyboard, focus, loading, empty, error, reduced-motion, and small-screen behavior.
- Add or update unit tests for validation, persistence, orchestration, recovery, and security changes.

## Pull requests

Every pull request should include:

- A concise description of the problem and solution.
- Screenshots or a short recording for meaningful UI changes.
- Tests run locally and any environment-specific limitations.
- Notes about migrations, compatibility, or security impact.

By submitting a contribution, you agree that it may be distributed under the repository's MIT License.
