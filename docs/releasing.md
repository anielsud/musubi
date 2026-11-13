# Releasing Musubi

Other people run Musubi on their own data and upgrade without us there
([data-safety.md](data-safety.md)). A release goes out only after it has
run on our own data.

## Every PR that changes what is stored

The PR template asks for it; in short ([data-safety.md](data-safety.md) §4):

- additive changes; `formatVersion` bumped for a one-way change,
  `schemaVersion` only when tables must be rebuilt;
- the description says what an existing dataset goes through, whether
  it is one-way, and what it costs in model calls;
- run on a copy of our dataset before merging:

  ```sh
  make build
  scripts/check-on-copy.sh ~/path/to/dataset/config.toml
  ```

  It clones the dataset, opens it with the new build (which upgrades the
  copy), runs an update and the knowledge plan, and compares the counts
  of items, statements, threads and the owner's corrections before and
  after, and the plan. It exits non-zero when either changed; the PR says
  why that is expected, or it isn't merged. (The recorder's sessions
  aren't re-read: the copy runs with a scratch HOME so nothing real is
  written.)

## A release

1. **Release candidate.** Tag main `vX.Y.Z-rc1` and push the tag. The
   `release` workflow builds the DMG and publishes a **pre-release**.
2. **Our dataset first.** Install the candidate on the owner's Mac (when
   no meeting is being recorded) and use it for a day of meetings. Check
   Status (recorder health, the work queue, "waiting from upgrades") and
   that the day's meetings arrived, were named and threaded.
3. **Fixture.** Add the release to `FIXTURES` in `scripts/fixtures.sh`
   (`vX.Y.Z:vX.Y.Z-rc1`), run `make fixtures`, and commit the new
   `testdata/fixtures/vX.Y.Z.db`: from then on CI checks that every later
   build upgrades a database this release wrote.
4. **Release.** Tag the same commit `vX.Y.Z`. Edit the release's notes:
   - what's new, in a few lines;
   - **Data changes**: what is migrated on first open, whether it is
     one-way (an older version can't read it afterwards), what it costs
     in model calls (the work queue's "waiting from upgrades"), and the
     copy made before (`backups/pre-upgrade-*.db`);
   - **Defaults changed**, if any, with how to keep the old behaviour;
   - known limits.
5. **Tell users** with the link; anyone coming from an earlier version or
   from Tomoe reads [upgrades.md](upgrades.md) first.

A candidate that misbehaves gets `-rc2`; the release is never re-tagged.
