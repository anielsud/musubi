Stack line, if stacked: `Stack, n of m: #a → #b → …`

**What and why**


**Data safety** ([docs/data-safety.md](../blob/main/docs/data-safety.md) §4) — delete if nothing stored, read or derived changes
- [ ] What an existing dataset (and recorder folder) goes through on upgrade:
- [ ] One-way? (an older version can't read the result) — if so, `formatVersion` bumped:
- [ ] Model calls on existing data, and their cost (they must wait for the owner):
- [ ] New behaviour has a config key that restores the old one:
- [ ] `scripts/check-on-copy.sh` on our dataset: counts and plan before → after:

**Tests**


🤖 Generated with [Claude Code](https://claude.com/claude-code)
