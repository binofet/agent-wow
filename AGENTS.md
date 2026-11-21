# Instructions for AI coding agents

- Do not edit README.md or any docs unless explicitly instructed.
- The environment may have a running Azerothcore server (authserver and worldserver) that can be used for end to end testing.
    - You are only allowed to interact with it via the public APIs (just like any client server model).
    - Do not change any of its internals (e.g. server configurations, database schemas or records, source code, etc).
    - You may refer to the source at https://github.com/azerothcore/azerothcore-wotlk to guide implementation of this client.

## Go harness and Lua behavior

- Go provides the game harness: transport, observations, reflected capabilities, and execution and validation of game actions. Keep gameplay decisions in Lua, including target ranking, pull plans, role choices, rotations, and tuning. Put a missing mechanic in Go as a reusable capability, then make the decision about when to use it in Lua.
- Use one Lua policy for each gameplay decision across planning and execution. For targeting, maintain the queue before a pull and through combat. The party leader shares the queue, including changes from Joel or raid marks. Keep target selection separate from the harness's checks for whether an attack can be executed.
- Companions can read, edit, test, and reload their own Lua policies through the existing self-edit workflow. Give agents the reflected observations and actions they need to do that directly. Verify a new policy revision and its effect in the same running character session. Gameplay tuning should take effect without recompiling Go or reconnecting a character.

## Keep verified behavior in main and in live releases

- Start each fix from the fork's current `main` or a branch explicitly integrating it. Put the complete fix and its focused check in a reviewable commit. Open or update a pull request to the fork's `main`, run its required checks, and verify the commit is reachable from `main` after integration. A successful local test, private branch, or live hotfix is not a merge.
- For a change to Go or the reflected API, release the client binary or image, `module.yaml`, `module.pb`, and required companion contracts as one version from the same tested source commit. For a Lua-only change, reload and verify the policy revision in the running client. For every exposed method, verify the manifest and descriptor agree with the binary. Preserve existing RPCs when rolling out an unrelated behavior fix. A binary-only swap can advertise a capability the live RPC registry cannot route.
- After a rollout, verify the affected live client can route the exact public RPC. For Resurrection, check `buddy-actions.resurrect_once` on Arlen and distinguish a normal precondition response from “method not found.” For healing, verify that a learned heal lands naturally when a teammate needs it. A restart or capability flag alone does not prove the behavior works.
- Record the source commit, artifact versions, live profile, and the checks that passed. If a live hotfix is necessary, use the same source integration path immediately so the next release preserves it. Keep unrelated in-progress work out of that release.

## Recovering a stuck companion inside an instance

- An online GM summon inside an instance can fail unless the GM is the target party leader. A sent command is not evidence that the character moved.
- Revive dead or released players **online**, then verify they are alive with full health before transport. Do not use offline `.summon` followed by offline `.revive`: corpse deletion and `AT_LOGIN_RESURRECT` processing after instance admission can send them outside.
- Only for a living stuck companion, use the managed launcher to log out normally, move the authenticated GM to the requested player, issue offline `.summon <companion>`, then restart the same profile. Wait for observed GM arrival and teleport completion before summoning; do not batch `.appear` with its dependent `.summon`.
- Scarlet Monastery's wings share map 189. Verify the wing, coordinates and actual distance to Joel; matching map IDs alone do not establish a regroup.
- At the Armory entry, online `.tele name <Name> SMArmory` reliably regrouped all four companions alive at full health within 18 yards of Joel. Verify the actual world positions, health and resumed following after recovery.
