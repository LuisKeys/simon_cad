## What and why

## Architectural invariant checklist

Every important feature, tool and abstraction must answer **yes** to all seven. If any answer is no, reconsider the abstraction before merging.

- [ ] 1. Can a human manipulate it visually?
- [ ] 2. Can an agent manipulate it semantically (a registered tool with a JSON schema)?
- [ ] 3. Is it represented deterministically (CAD-IR, canonical serialization)?
- [ ] 4. Can it be replayed (commands in a ChangeSet, `simoncad replay`)?
- [ ] 5. Can it be undone (every command has an exact inverse)?
- [ ] 6. Can it be validated (schema, semantic, rebuild, geometry)?
- [ ] 7. Can it be explained (ChangeSet explanation, intent, provenance)?

## Mutation paths
- [ ] No new way to change the model that bypasses tool → command → ChangeSet.
- [ ] No OCCT type appears outside `internal/geometry/bridge`.

## Tests
- [ ] `make test` (C++ bridge test, vet, Go tests incl. recorded evals)
- [ ] `make leakcheck` if the native layer changed
- [ ] Robot requirement behind this change (or none — backlog items without one are deprioritized)
