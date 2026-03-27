---
name: refactor-worker
description: Go backend refactoring worker — structural cleanup, error handling, deduplication
---

# Refactor Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Any feature involving Go backend code cleanup: dead code removal, error handling fixes, constant extraction, file splitting, package restructuring, code deduplication.

## Required Skills

None. All validation is through shell commands (go build, go vet, go test, grep).

## Work Procedure

### 1. Understand the Feature
- Read the feature description, preconditions, expectedBehavior, and verificationSteps carefully.
- Read `AGENTS.md` for mission boundaries and coding conventions.
- Read `.factory/library/architecture.md` for package structure and import rules.
- Identify all files that will be modified.

### 2. Pre-Change Baseline
- Run `go build ./...` to confirm the project compiles.
- Run `go test ./...` to confirm all tests pass.
- If either fails, STOP and return to orchestrator — do not proceed on a broken baseline.

### 3. Plan the Changes
Before editing, plan:
- Which files will be created, modified, or deleted?
- For file splits: which functions/types move where? What are the new file names?
- For package splits: what are the new package names? How will imports change?
- For error wrapping: which functions need wrapping? Will wrapping affect any `errors.Is`/`errors.As` callers?

### 4. Implement Incrementally
- Make changes in small, logical batches (e.g., one file split at a time, one package at a time).
- After each batch, run `go build ./...` to catch compilation errors immediately.
- Fix any issues before proceeding to the next batch.

### 5. For File Splits Specifically
- Keep the main type/struct definition and constructor in the original file.
- Create new files named after the concern being extracted (e.g., `health_check.go`, `migration.go`, `vsock.go`).
- Move entire functions — do not split functions across files.
- Move associated test functions to matching test files.
- Verify the original file's line count decreased as expected.

### 6. For Package Splits Specifically
- Create sub-package directories first.
- Move files to sub-packages, updating their `package` declaration.
- Update all import paths across the codebase.
- If the parent package needs to expose sub-package functionality, use delegation (call sub-package functions from thin wrappers in the parent).
- Run `go build ./...` after each sub-package move to catch import cycles immediately.

### 7. For Error Wrapping Specifically
- Use `fmt.Errorf("descriptiveContext: %w", err)` pattern.
- Skip one-liner delegating functions where the callee already provides context.
- Check if any callers use `errors.Is()` or `errors.As()` — wrapping with `%w` preserves this, so it's safe. But do NOT change error types.
- Grep for the function name + "errors.Is" or "errors.As" if unsure.

### 8. Final Verification
- Run `go build ./...` — must exit 0.
- Run `go vet ./...` — must exit 0.
- Run `go test ./...` — must exit 0, all tests pass.
- Run any feature-specific verification steps from the feature description.
- Verify each expectedBehavior item is met.

### 9. Commit
- Commit with a descriptive message covering what was refactored.
- Do not include unrelated changes.

## Example Handoff

```json
{
  "salientSummary": "Extracted 6 duplicate uuidToPgtype/pgUUID definitions from deploy, builder, vmm, server, volumeops, and migrationreconcile into internal/shared/pguuid package. Updated all 8 importing packages. go build/vet/test all pass.",
  "whatWasImplemented": "Created internal/shared/pguuid/pguuid.go with exported ToPgtype, FromPgtype, Equal, and ToString functions. Removed duplicate definitions from 6 packages. Updated all import paths. Removed duplicate pgUUID test helpers from 3 test files, replaced with imports from shared package.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      { "command": "go build ./...", "exitCode": 0, "observation": "Clean compilation, no errors" },
      { "command": "go vet ./...", "exitCode": 0, "observation": "No vet diagnostics" },
      { "command": "go test ./...", "exitCode": 0, "observation": "All 47 tests pass across 14 packages" },
      { "command": "grep -rn 'func uuidToPgtype\\|func pgUUID\\b' internal/ --include='*.go' | grep -v '_test\\.go' | grep -v 'internal/shared/'", "exitCode": 1, "observation": "No duplicate definitions found outside shared/" },
      { "command": "grep -rn 'internal/shared/pguuid' internal/ --include='*.go' | grep -v '_test\\.go' | wc -l", "exitCode": 0, "observation": "8 packages now import from shared/pguuid" }
    ],
    "interactiveChecks": []
  },
  "tests": {
    "added": []
  },
  "discoveredIssues": []
}
```

## When to Return to Orchestrator

- A file split would require changing public API behavior (not just moving code)
- Import cycles that cannot be resolved without architectural changes
- Test failures that indicate a behavioral change (not just compilation errors from moves)
- The baseline (go build/test) is already broken before your changes
- Ambiguity about whether a change is structural or behavioral
