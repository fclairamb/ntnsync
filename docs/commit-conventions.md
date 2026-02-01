# Commit Conventions

This project uses [Conventional Commits](https://www.conventionalcommits.org/) to automate releases and generate changelogs.

## Format

```
<type>(<scope>): <description>
```

Example: `feat(sync): add incremental sync support`

## Commit Types

### Visible in Changelog

These types appear in release notes:

| Type | When to Use | Example |
|------|-------------|---------|
| `feat` | New feature or capability | `feat(cli): add --dry-run flag` |
| `fix` | Bug fix | `fix(sync): handle empty pages` |
| `perf` | Performance improvement | `perf(notion): batch API requests` |
| `refactor` | Code restructuring without behavior change | `refactor(store): extract git operations` |
| `style` | Code style changes (formatting, semicolons) | `style(converter): fix indentation` |

### Hidden from Changelog

These types trigger releases but don't appear in notes:

| Type | When to Use | Example |
|------|-------------|---------|
| `build` | Build system or external dependencies | `build: update Go to 1.25` |
| `docs` | Documentation only | `docs: update CLI examples` |
| `chore` | Maintenance tasks | `chore: update .gitignore` |
| `test` | Adding or fixing tests | `test(sync): add edge case tests` |
| `ci` | CI/CD configuration | `ci: add code coverage` |
| `revert` | Reverting a previous commit | `revert: undo feature X` |
| `release` | Release-related changes | `release: prepare v1.0.0` |

## Scopes

Scopes are optional but recommended:

- `cli` - Command-line interface
- `notion` - Notion API interactions
- `sync` - Sync logic
- `store` - Git store operations
- `converter` - Markdown conversion
- `docker` - Docker configuration
- `webhook` - Webhook server
- `deps` - Dependencies
- `docs` - Documentation
- `ci` - CI/CD
- `release` - Release process
- `logging` - Logging infrastructure

## Choosing the Right Type

**feat vs fix**: Did you add something new, or fix something broken?
- Adding a new flag → `feat`
- Fixing a flag that wasn't working → `fix`

**refactor vs fix**: Does the behavior change?
- Same behavior, cleaner code → `refactor`
- Different/correct behavior → `fix`

**refactor vs perf**: Why are you restructuring?
- For code clarity → `refactor`
- For speed/memory → `perf`

**chore vs build**: What are you changing?
- Build scripts, Dockerfile, go.mod → `build`
- Other maintenance (.gitignore, editor config) → `chore`

**ci vs build**: Where does it run?
- GitHub Actions, CI pipelines → `ci`
- Local build process → `build`
