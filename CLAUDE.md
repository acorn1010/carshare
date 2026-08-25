# Carshare Guidelines

Standalone open-source repo. Public means: no secrets, no internal names, no references to
private documents. Secrets live in `~/.config/carshare/` and env vars, never in the repo.

## Writing (README, docs, comments, commit messages)

Write like a person, not an AI.

- Short, plain words. No semicolons, no em-dashes. Say "grouped", not "coalesce".
- Describe, don't perform. No metaphors invented for flavor ("the site is its shop window"),
  no wry asides ("the best fifteen minutes in this repo"), no rhythmic closers ("everything
  below is commentary"). Say what the thing is and what it does.
- The test for any phrase: would a person say it out loud?
- Assert only what you measured. A number in prose comes from a benchmark or a query, or it
  goes. Date anything that drifts ("as of 2026-08, the demo seeds 725k cars").
- Cut what the page already shows. No empty transitions, no colon taglines on headings.
- Code comments state a durable fact or constraint. No runbooks, no narrative, no comments
  that explain the diff to a reviewer.

## Working

- Don't assume, don't hide confusion. State your assumptions. If multiple interpretations
  exist, present them instead of picking one silently. If a simpler approach exists, say so.
- Simplicity first: smallest working version, no speculative flags, modes, or abstractions.
  A few lines in one place beat a new file of indirection.
- Surgical changes: touch only what the task needs, match the surrounding style. Remove
  imports and functions your change orphaned, leave pre-existing dead code alone.

## Code
- Private and `const` by default. Export only what another package uses.
- Descriptive names. Curly braces on every conditional and loop, even one-liners.
- Go: stdlib over dependencies. All SQL lives in `internal/store`. Every exported symbol
  has a doc comment saying what it does and why it exists.
- Web (`web/`): functional React components, TypeScript `type` over `interface`, Tailwind
  utility classes only, trailing commas.
- Schema changes are declarative: edit `db/schema.sql` to the desired end state, apply with
  `./db/update_schema.sh`. Never write migration scripts.

## Verification

- `make test-sql` runs everything including the concurrency suite (needs `./scripts/dev_db.sh`).
- Say how far each claim got: pointed at the line, walked the logic step by step, ran a test
  that fails loudly if wrong, or saw it in the running app. An unlabeled guess reads exactly
  like a proof. "The other callers do the same" is a guess until you run one.
- A check that ran is not a check that passed. A skipped test, a blank screenshot, or a
  generator that processed no files exits clean and answers nothing.
- Correctness claims about booking races belong in `internal/store/integration_test.go`.

## Logging

- Log directly and unconditionally. Never wrap logs in a debug flag, filter with grep instead.
- Error logs carry the data that identifies what failed (car id, query, offending value), not
  just the error string.

## Git

- Don't commit or push until asked. Break work into focused, reviewable commits.
