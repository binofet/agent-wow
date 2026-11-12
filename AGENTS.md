# Instructions for AI coding agents

- Do not edit README.md or any docs unless explicitly instructed.
- The environment may have a running Azerothcore server (authserver and worldserver) that can be used for end to end testing.
    - You are only allowed to interact with it via the public APIs (just like any client server model).
    - Do not change any of its internals (e.g. server configurations, database schemas or records, source code, etc).
    - You may refer to the source at https://github.com/azerothcore/azerothcore-wotlk to guide implementation of this client.

## Keep verified behavior in main and in live releases

- Start each fix from the fork's current `main` or a branch explicitly integrating it. Put the complete fix and its focused check in a reviewable commit. Open or update a pull request to the fork's `main`, run its required checks, and verify the commit is reachable from `main` after integration. A successful local test, private branch, or live hotfix is not a merge.
- Release the client binary or image, `module.yaml`, `module.pb`, and required companion contracts as one version from the same tested source commit. For every exposed method, verify the manifest and descriptor agree with the binary. Preserve existing RPCs when rolling out an unrelated behavior fix. A binary-only swap can advertise a capability the live RPC registry cannot route.
- After a rollout, verify the affected live client can route the exact public RPC. For Resurrection, check `buddy-actions.resurrect_once` on Arlen and distinguish a normal precondition response from “method not found.” For healing, check her enabled state and verify a normal heal when Joel needs it. A restart or capability flag alone does not prove the behavior works.
- Record the source commit, artifact versions, live profile, and the checks that passed. If a live hotfix is necessary, use the same source integration path immediately so the next release preserves it. Keep unrelated in-progress work out of that release.
