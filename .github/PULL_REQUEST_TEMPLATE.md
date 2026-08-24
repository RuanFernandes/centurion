## What changed?

<!-- Describe the problem and the user-visible result. -->

## Why?

<!-- Link an issue or explain the context. -->

## Validation

- [ ] go test ./...
- [ ] npm --prefix frontend run build
- [ ] wails3 build (when the change affects desktop packaging or native code)

## Checklist

- [ ] No credentials, local databases, workspace files, or build output are included.
- [ ] Persisted schema changes include a migration.
- [ ] Security and approval boundaries were preserved.
- [ ] UI changes work with keyboard navigation and reduced motion.
- [ ] Screenshots or a recording are included for meaningful UI changes.
