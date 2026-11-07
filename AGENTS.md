## Planning

- At the start of each repository task, read the applicable `AGENTS.md` files, including any nested instructions for the files involved. Re-read them if they change during the task.
- Ask for clarification before implementation when instructions or intended behavior are ambiguous, or the change could unintentionally expand the requested scope.
- Confirm before adding work outside the requested scope. Necessary steps within the requested scope do not require separate confirmation.
- Make the smallest complete change needed. Avoid unrelated cleanup, speculative features, and unnecessary abstractions.

## Specifications

- Use `spec.yaml` as the API contract and align API implementations with it.
- For non-API behavior, use the relevant existing specification. Ask where to document the behavior if no applicable specification exists.
- Update relevant specifications before implementing behavior or API contract changes. For refactors and fixes that restore specified behavior, verify the specifications without requiring an edit.

## Dependencies and implementation

- Prefer the Go standard library and existing dependencies. Add dependencies only when necessary, and verify that they are pure Go, actively maintained, and not deprecated. Do not judge maintenance solely by recent release dates.
- Use only pure Go libraries. Avoid obsolete or deprecated packages and functions, including in existing dependencies.
- Follow `.golangci.yml` during implementation.
- Keep production store SQL in descriptive `.sql` files and embed them with `//go:embed`; do not inline SQL statements in Go source or load SQL from the filesystem at runtime.
- Keep SQL values parameterized through driver arguments; do not interpolate request values into SQL text.
- Favor maintained APIs and straightforward designs that avoid brittle assumptions. Future-proofing must not expand the requested scope.
- Log actionable debugging context and useful operational fields for metrics while protecting personally identifiable information (PII) and secrets.

## Validation

- Write unit tests for essential code paths, including relevant success cases, failures, and edge cases.
- Only write integration tests when explicitly requested.
- Run relevant unit tests and the project's configured lint checks before reporting completion. Report any checks that could not run.
