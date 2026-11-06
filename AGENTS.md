## Planning

- Ask for additional context on ambiguous instructions or that could possibly result to an unwanted wider scope for implementation
- It is better to keep things minimal, instead of overdoing everything
- When a task makes sense to do at that stage, confirm first before adding them to the plan
- Always update the specifications before the code generation
- Always use pure Go libraries

## Code generation

- Align the code generation with the API spec and linters in the project
- Do not use obsolete packages or functions
- Lean towards future-proofing
- Log useful error details (especially those that can be used for metrics) while ensuring proper handling for PIIs
- Write unit tests for essential code paths and scenarios
- Only write integration tests when told