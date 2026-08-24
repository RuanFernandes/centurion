# Security policy

## Scope

Centurion is a local desktop application that launches and communicates with the Codex App Server. The most important security boundaries are workspace paths, local tools, MCP integrations, approvals, persisted diagnostics, and account state.

## Reporting a vulnerability

Please do not open a public issue for a suspected vulnerability. Use GitHub's private security advisory flow for this repository whenever it is available. Include:

- A clear description of the issue and affected version or commit.
- Reproduction steps or a minimal proof of concept.
- Potential impact and whether credentials, files, or external systems are involved.
- Any suggested mitigation.

Please allow reasonable time for investigation before public disclosure.

## Security expectations for contributors

- Do not commit API keys, ChatGPT tokens, MCP OAuth credentials, cookies, environment files, databases, or private workspace files.
- Keep redaction tests updated when diagnostic formats change.
- Validate and normalize paths before using them.
- Do not bypass approval policies or rate-limit guards for convenience.
- Treat new MCP or shell capabilities as security-sensitive changes.
