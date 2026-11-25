# Create a character on Wrath

Use the normal `agent-wow char create` client command on the intended account's
character-selection connection. This does not require GM privileges or changes
to the realm's console, database, or configuration. It is not an in-world
`buddy-*` RPC.

This procedure was checked against source commit
`742b9649b7fd901feb613a78ee93c7864e24708f` on 25 November 2026. Check the installed
binary's help too: source support does not establish what an older binary can do.

## 1. Use the intended account's existing profile

- Use the existing, operator-managed Wrath connection/tunnel and account. Do not
  guess a server address or realm ID.
- Obtain the profile paths from its existing managed configuration. Do not read,
  print, copy, or put passwords/session keys into commands or this document.
- Use an idle account at character selection. If it is already in the world,
  preserve its current work and arrange a normal managed logout of that account
  only when appropriate. Do not kill a live client or replace its authentication
  just to make this procedure work.
- Keep any previous create request, outcome, and pending/UNKNOWN record. A new
  shell invocation does not reset that custody.

`AGENT_WOW_*` environment values override `--config`. Config and data directories
also have shared defaults, so `--config` alone is not sufficient profile isolation.
In Bash, fill these placeholders with the intended profile's **already-known**
absolute paths, then use the same wrapper for every command below:

```bash
CHAR_CLIENT='/path/to/installed/agent-wow'
CHAR_CONFIG='/path/to/intended/client.yaml'
CHAR_CONFIG_DIR='/path/to/intended/config-directory'
CHAR_DATA_DIR='/path/to/intended/data-directory'
CHAR_AUTH_FILE='/path/to/intended/saved-auth-file'
CHAR_REALM_FILE='/path/to/intended/saved-realm-file'

aw=(env -i PATH="$PATH" HOME="$HOME"
  AGENT_WOW_CONFIG_DIR="$CHAR_CONFIG_DIR"
  AGENT_WOW_DATA_DIR="$CHAR_DATA_DIR"
  AGENT_WOW_AUTH_FILE_PATH="$CHAR_AUTH_FILE"
  AGENT_WOW_REALM_FILE_PATH="$CHAR_REALM_FILE"
  "$CHAR_CLIENT" --config "$CHAR_CONFIG")
```

The YAML must supply the profile's maintained Wrath authserver/tunnel endpoint.
If that profile supplies required endpoint settings through environment overrides
instead, add those **known, approved** `AGENT_WOW_AUTHSERVER_HOST` and
`AGENT_WOW_AUTHSERVER_PORT` assignments to the wrapper before `"$CHAR_CLIENT"`.
Do not silently fall back to the client's default endpoint. Auth/realm filenames
are often `auth.json` and `realm.json`, but use the actual profile paths rather
than assuming those conventions.

## 2. Check the command, account, realm, and existing characters

```bash
"${aw[@]}" char create --help
"${aw[@]}" char list --help
"${aw[@]}" auth status --timeout 15s
"${aw[@]}" realm list --json --timeout 15s
"${aw[@]}" realm status --timeout 15s
"${aw[@]}" char list --json --timeout 15s
```

Verify the saved account identity, the selected Wrath realm, and the pre-create
character list. Retain a bounded operator record of the intended name, race,
class, gender, realm, pre-list, and any pending attempt; keep secrets out of it.

If the saved session is invalid and the account is eligible for login, use
`"${aw[@]}" auth login --timeout 15s` with its existing managed credential source.
Then repeat the checks. If realm selection needs correction, use
`"${aw[@]}" realm set '<actual ID or exact name from realm list>' --timeout 15s`
and verify `realm status` again. A realm can have a display name other than
"Wrath"; use the operator's actual configured realm, not a hardcoded example.

If the intended name already exists, reconcile that character's identity first.
Do not create another name, delete a character, or switch accounts to get around
an unresolved attempt or an identity mismatch.

## 3. Send one explicit create request

**The following command creates a character; it is not a dry run and has no
additional confirmation prompt.** Replace the example identity with the one
requested for this account:

```bash
"${aw[@]}" char create \
  --name Newmage \
  --race human \
  --class mage \
  --gender female \
  --timeout 15s
```

For planned companions, specify name/race/class/gender explicitly. Every omitted
option is randomized using stock 3.3.5a compatibility data. An explicit `0` is
preserved rather than treated as omitted.

Optional appearance flags are `--skin`, `--face`, `--hair-style`, `--hair-color`,
and `--facial-hair`; each accepts an unsigned byte index. For example,
`--hair-color 0` explicitly chooses index zero. Omitted appearance is randomized.
The realm still enforces its account, name, faction, expansion, race/class, and
other creation restrictions.

Supported race names: `human`, `orc`, `dwarf`, `night-elf`, `undead`, `tauren`,
`gnome`, `troll`, `blood-elf`, `draenei`.

Supported class names: `warrior`, `paladin`, `hunter`, `rogue`, `priest`,
`death-knight`, `shaman`, `mage`, `warlock`, `druid`.

Gender accepts `male`/`0` or `female`/`1`. Known numeric race/class protocol IDs
are also accepted; names make an operator request easier to review.

## 4. Verify the result before doing anything else

```bash
"${aw[@]}" char list --json --timeout 15s
```

Use the **same wrapper, account, and realm**. Match the intended name, race,
class, and gender; record its returned string GUID and the observed realm.
Successful create output confirms the server result and resolved appearance,
but does not return a GUID. `char list --json` supplies GUID and identity; it
does not include appearance fields. Keep the confirmed create output when
appearance matters, and do not invent appearance verification from that JSON.

Character creation does not enter the world, create a managed gameplay profile,
attach Lua, invite a party, level/train/equip the character, or start a model
worker. Those are separate tasks. Use the existing managed launcher for later
gameplay; `char play` would itself start persistent gameplay/RPC activity.

## Failures and uncertain outcomes

| Observation | Next action |
| --- | --- |
| Installed binary lacks `char create` or `char list --json` | Report its actual help/error and capability gap. Do not substitute a GM/server command or silently replace a running installation. |
| Account already online | Reconcile the intended account's live session; preserve it until an appropriate normal managed logout. |
| Auth/tunnel/realm check fails | Stop before creation and correct the existing connection/session context. Do not guess endpoints, accounts, or realm IDs. |
| Definite server rejection | Retain the exact result and address its stated reason. An explicit `--name` has no automatic name retry. |
| Timeout, lost reply/output, partial write, malformed reply, or `outcome unknown` | Retain the original attempt as UNKNOWN. Run **only `char list --json`** next and reconcile; do not automatically issue another create. |

If readback finds the intended identity, record its observed GUID and do not
create again. If readback is absent, fails, or disagrees, retain the unresolved
record and stop automatic recreation. Absence alone is not proof that the first
request never ran. Do not use a fresh name or an expired local timeout to bypass
uncertainty. The CLI has no durable no-replay journal across separate invocations;
the original operator must preserve that request/result custody.

## Implementation references

- [CLI creation flags and success output](../cmd/char_create.go)
- [Account/profile configuration and environment precedence](../internal/config/config.go)
- [Realm selection](../cmd/realm.go)
- [Option resolution and bounded name retries](../pkg/char/create.go)
- [Create response and UNKNOWN handling](../pkg/char/characters.go)
- [Uncertain-outcome error guidance](../pkg/char/errors.go)
- [Character-list JSON fields](../internal/output/list.go)
