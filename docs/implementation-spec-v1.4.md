# WORLD implementation target v1.4

**Status:** adopted target design for future WORLD work. It does not describe a deployed service or an adopted cross-module API. Current source foundations are [R05](R05.md) (inspect), [R06](R06.md) (EventStore), and [R07](R07.md) (explicit-config HTTP); the R07 routes are the provisional table below and are not adopted.

This document adopts the WORLD-owned portion of the v1.4.0 review bundle. Official source archive path on host 206: `~/RenCrow/Tmp/WORLD/WORLD_実装仕様一式_v1.4.0_20261020.zip`. SHA-256: `f5e69cdca00e3551656dcd5839355802b9cebf03989d778861f176b90907559f`. Byte-identical, noncanonical snapshots and member hashes are indexed in [the reference README](references/world-v1.4.0/README.md): [integrated specification](references/world-v1.4.0/WORLD_Integrated_Spec_v1.4.0.md), [acceptance matrix](references/world-v1.4.0/contracts/acceptance_matrix.json), [WORLD schema](references/world-v1.4.0/contracts/world.schema.json), [migration notes](references/world-v1.4.0/migration/README.md), [WorkSet v2 schema](references/world-v1.4.0/inputs/rencrow_final_20261019/contracts/work-set-v2.schema.json), [Isekai current settings appendix](references/world-v1.4.0/appendices/01_Isekai_Current_Settings.md), and [RenCrow living world appendix](references/world-v1.4.0/appendices/03_RenCrow_LivingWorld.md). Acceptance IDs below refer to the matrix snapshot; all 24 remain `not_run` there.

## 1. Ownership and current compatibility

WORLD owns world definitions, immutable published World Bundles, world and Timeline state, deterministic rules, spatial state, reservations, events, snapshots, receipts, and observation candidates. The current R05 command remains a one-shot, read-only inspection. R05 has no listener, durable store, external provider, LLM call, or production WorldSnapshot.

CORE owns Agent identity, Persona, intent, Task/Run, policy, conversation, speech content, and memory. WORLD may retain versioned references and accepted results, but it does not make second copies or write to CORE memory. StoryPlayer Authoring owns stories, source text, plot, choices, and narrative releases. This document specifies only WORLD references and command/read interfaces to StoryPlayer. PORTAL owns rendering and playback presentation; a view or camera change never changes WORLD state.

The eight R05 resident IDs remain present in the `rencrow-home` HOUSE profile: `mio`, `shiro`, `kuro`, `midori`, `aka`, `ao`, `kin`, `gin`. Lumina remains an absent-by-default Visitor admitted only by an explicit authorized visit. The four added HOUSE residents Aka, Ao, Kin, and Gin are WORLD entities; their presence in R05 output does not create CORE Agent identities, credentials, Tasks, or authority. Actual RenCrow Agents remain those registered by CORE (currently Mio, Shiro, Kuro, and Midori); only the authenticated user and CORE-managed Agents are Actors under the repository-wide identity rule.

R05 compatibility requirements:

- Keep the R05 JSON input checks, explicit `--config`, `--world-id`, and `--at` arguments, exact output fields, and read-only behavior. The result remains exactly `schema_version: 1`, `world_id`, sorted `resident_actor_ids` (the eight IDs listed above), `visitor_present: false`, `world_time_utc`, `world_time_local`, `spatial_ready: false`, and `world_revision: 0`. This is not a committed State or Event sequence.
- Keep all eight resident IDs and the Visitor rules. Do not turn the eight-resident HOUSE profile into a generic Kernel constant.
- Preserve R05 WorldID and entity IDs as exact valid UTF-8 opaque identifiers: no trimming, case folding, Unicode normalization, or aliasing. Continue to reject invalid UTF-8, surrounding whitespace, and controls as R05 specifies.
- Do not change `spatial_ready: false` or fabricate coordinates. `position: null` and `measurement_status: unverified` remain unknown until measured geometry is accepted.
- New persisted version/scope fields (`world_spec_revision`, `timeline_epoch`, `event_seq`, `state_seq`) are distinct from R05 `world_revision`.

**Contract issue to resolve before freezing R06/R07 schemas:** the v1.4 contract README specifies ASCII machine IDs, while R05 identifiers are Unicode-capable opaque values. This target preserves R05 exact-ID behavior. Any ASCII transport identifier must use an explicitly specified, collision-free, reversible mapping that retains the original ID; normalization or an undocumented alias is not acceptable. The final schema, mapping, and cross-language tests are pending. Until then, R05 remains valid and no new API may claim complete R05 identifier compatibility.

## 2. WORLD target model

Use one generic Go Kernel with per-world configuration, not HOUSE or Isekai rules compiled as global defaults. Keep each initial WorldInstance's writer, authorization scope, resources, database, event stream, and Timeline namespace isolated. The initial deployment candidate is a separate process and Store per WorldInstance; sharing a process later must preserve the same isolation.

A published execution target consists of:

1. **LoreRevision:** sourced claims, author adoption status, claim type, scope, and supersession history.
2. **ExecutionProfile:** registered rule versions, clock, limits, spatial data, assumptions, and exposed capabilities.
3. **PublishedWorldBundle:** an immutable, hashed reference to the validated LoreRevision, ExecutionProfile, schemas, and dependencies.

Author adoption, whether a claim is fact/belief/hypothesis/observation/unset, who may know or view it, and whether it is validated/published are separate fields. Missing values stay missing. A capability that requires an unset value is unavailable (`needs_parameter`); unrelated validated capabilities may still run. An implementation assumption or derived value keeps its source and transitive `assumption_ref` and cannot silently become author canon.

Publishing creates a new immutable Bundle and does not retarget a running Timeline. Applying a Bundle is an explicit authorized `adopt_world_bundle` command. It validates the old and new Bundle, current State, rules, activities, and reservations, then records the change as an Event. If safe migration cannot be proven, create a separate Timeline. Never rewrite old snapshots or events.

Use Timeline purpose (`live`, `fiction`, `simulation`, `rehearsal`, `performance`) separately from canon scope/status and memory writeback policy. Default cross-Timeline and fiction-to-Live memory writeback is prohibited. A fork pins its parent Snapshot and Event cursor and, when relevant, a CORE-owned `ContextSnapshot` reference with version and hash. Child state and context remain isolated; parent changes are never merged automatically.

### 2.1 Import contract for existing WORLD settings

Treat source inputs as immutable. A future import writes a separate normalized view with original path/hash/record ID, JSON pointer or section, source status, owner, adoption, scope, and supersession evidence. The v1.3.5 Isekai WORLD registry's 165 records are sourced setting records, not 165 executable rules; retain all original IDs and import every record as unpublished. Only author-adopted claims with valid scope and complete executable parameters may enter a published Bundle; other claims remain available as draft, hypothesis, or unset input. Preserve later author decisions separately from experiment assumptions and derived values. Date alone does not supersede a decision; resolve by claim, scope, source, and explicit supersession.

Preserve `world_fantasy_engineer_001`, `loc_starting_town`, the three draft entities `ent_protagonist`, `ent_benefactor`, `ent_apprentice`, and `obj_benefactor_magic_instrument` with their source status. The town's existence does not decide its name or internal layout; NPC drafts do not imply CORE registration; the instrument does not imply a failure cause. The source has zero Action/Rule definitions, no initial State/Snapshot/Event, and no published World revision: import must not invent these. Keep the separate CHAR (9 records) and StoryPlayer (48 records) registries with their owners. Treat unset `public_background` visibility as non-public until fact-level visibility is defined; this is a delivery default, not a new assertion about who knows the fact.

For `rencrow-home`, the HOUSE Profile owns the eight resident entities and the room/prop topology. The source layout places workplace, stairs, living area, and kitchen on 1F; four private rooms on 2F; and a shared attic area for Aka, Ao, Kin, and Gin. A logical room graph does not prove measured geometry: keep `logical_ready`, `spatial_ready`, and `visual_accepted` separate. Unmeasured coordinates remain null; measured navigation and occupancy belong to R11, final art acceptance to R14.

## 3. Identity, versions, time, and storage contract

Every command, event, projection, receipt, snapshot, and reference is scoped to a `world_id`; Timeline-dependent records also carry `timeline_id` and the destination `timeline_epoch`.

| Field | Meaning and rule |
|---|---|
| `contract_version` | Wire/schema version; reject an unknown major version. |
| `world_spec_revision` | Immutable published Bundle revision, never a mutable State counter. |
| `timeline_epoch` | Opaque history-generation ID. Keep across ordinary restart; replace after restore to an earlier point. Do not order it lexically. |
| `event_seq` | Contiguous committed Event order within a Timeline epoch. |
| `state_seq` | Last Event sequence incorporated into the State projection. |
| `source_epoch` / `source_revision` | CORE projection producer generation and per-actor/type sequence. Never compare with WORLD `event_seq`. |
| `view_cursor` | Opaque, permission-scoped position in a viewer projection; never expose private Event gaps. |

New contracts do not use an unqualified `world_revision`. Public JSON integer sequences stay within `0..2^53-1`. Each Event has a unique ID and inherits the parent transaction scope. Each multi-Event transaction has one ID and contiguous sequence numbers. Timestamps distinguish WORLD `recorded_at`, externally sourced `occurred_at`, and Timeline `world_time`; only WORLD rules set new world time.

The event log is authoritative; State is a deterministic projection. The initial Store candidate is local SQLite WAL with one logical writer, subject to R06 confirmation against existing WORLD dependencies and the supported database build on all three OSes. Event, State, reservations, and Receipt commit atomically. Do not use a live database file copy as a backup. Snapshot plus subsequent Events must reproduce the same canonical State hash. Replay applies saved Events; it never reruns an LLM, external Tool, or current rule to invent past outcomes.

## 4. WORLD Command, policy, and receipt behavior

The following order and receipt rules apply to a WORLD Command only; CORE projections use the separate inbox contract below.

1. Authenticate the caller and derive its capabilities from the authenticated principal; never grant authority from a payload `actor_id`, `world_id`, or claimed owner.
2. Validate contract version, schema, UTF-8/JSON, body and record limits, exact world/Timeline/epoch scope, and registered action/payload schemas.
3. Check idempotency and payload digest. The namespace is `world_id + timeline_id + timeline_epoch + authenticated_client_id + idempotency_key`; use canonical JSON (RFC 8785 JCS) plus SHA-256 for the request body.
4. Validate Bundle revision, current State, expiry, required dependencies, permission, resources, path, visibility, and rule parameters.
5. Run a registered, typed, versioned Go handler with injected clock and deterministic random source. Check resulting invariants.
6. Atomically commit the ordered Event group, State projection, reservation/activity changes, and Receipt. Publish only after commit.

The same idempotency key and body returns the same Receipt and Event references without a second effect. The same key with another body is `409 idempotency_conflict`. Strict edits and turn-based actions use `expected_state_seq`; mismatch is `409 state_conflict`. Real-time intents revalidate the complete server-defined dependency/resource set; a meaning-changing dependency mismatch is `409 dependency_conflict`. A client cannot choose an arbitrary handler or State delta.

Use these Command result boundaries:

- Malformed JSON, duplicate keys, unauthenticated requests, and requests whose scope or idempotency namespace cannot be established are transport errors recorded in bounded audit logs; they do not receive a WORLD Receipt.
- An authenticated, structurally valid command that fails authorization, scope, reference, expiry, or precondition checks receives a `rejected` Receipt and changes no State.
- A valid world action that is attempted and fails in-world may be `committed` with an `attempt_failed` Event when the attempt itself consumes time or has another world effect.
- A committed Receipt refers to its transaction and Events. A rejected Receipt has no transaction/Event references. Error detail returned to an Actor must not reveal private entity existence or hidden facts; privileged audit detail uses a separate scope.

Command transactions are limited to 128 Events and 256 KiB serialized. Event pages are limited to 500 Events and 2 MiB and never split a transaction. Reject an oversized atomic action before commit or model it as an explicitly resumable Activity; never silently split a reservation transaction.

### CORE projection inbox: separate ordering and idempotency boundary

A CORE projection is not a WORLD Command and is not governed by the Command idempotency key, `expected_state_seq`, or Command Receipt rules. Authenticate the CORE publisher and resolve the exact destination scope (`world_id`, `timeline_id`, `timeline_epoch`) before considering source order. Then validate `actor_id` and its authorized mapping to `actor_instance_id`, `projection_kind`, `source_epoch`, `source_revision`, payload schema, and canonical payload digest.

Ordering and idempotency are scoped by destination, Actor mapping, projection type, and source epoch. The same scope/epoch/revision and digest is an idempotent retry; the same scope/epoch/revision with a different digest is a conflict; an older `source_revision` is rejected. Treat `source_epoch` as opaque: accept an epoch change only through authenticated reconciliation with a fresh full projection, never by string or numeric comparison or by relabeling a pending payload. Extending a projection's validity requires a new `source_revision`. Projection acknowledgements/conflicts use the projection contract and do not fabricate a WORLD Command Receipt or compare source revisions to `event_seq`.

Rule definitions are registered typed handlers (`handler_id`, `handler_version`, typed parameters). Do not evaluate code, shell, or arbitrary expressions from JSON/YAML/natural language. LLM-generated rule ideas remain proposals and require deterministic schema/semantic checks plus explicit authorized publication. The Kernel makes no LLM, HTTP, Tool, rendering, or physical-world call during a transition.

## 5. Activities, CORE interface, privacy, and recovery

An Actor has at most one primary WORLD life Activity. Long-running activities record plan, start, state, due time, resources, reservation, interruption, completion, and cancellation. Store meaningful transitions, not animation frames. A real CORE Task/Run remains CORE-owned: a WorkSet disappearing is not proof of success, and WORLD movement or posture never creates a work result.

CORE is the authenticated writer for its scoped LifePolicy, full WorkSet v2, activity intents, confirmed utterance references, and admitted visitor operations. Preserve WorkSet v2 semantics unchanged: queued may have no Run and does not start work movement; running requires a Run and may start presence movement only when its projection requires it; waiting/blocked remains waiting; ordinary chat does not force a move; multiple work items remain independent; a fresh full empty Set means no current assigned work but not success; a stale or expired WorkSet means unknown and cannot imply completion. A confirmed utterance supplies IDs, permitted listeners, occurrence time, and a WORLD history cursor for location; WORLD stores no speech body. Keep CORE `source_epoch/source_revision` ordering, same-version digest checks, and durable outbox/reconcile at CORE's owner boundary. Exact projection permission and LifePolicy schema still require CORE adoption.

**Future v1.4 target behavior, not current R05 behavior:** while CORE is unavailable, WORLD may continue only non-cognitive activities allowed by the stored, still-valid LifePolicy. After that policy expires, it may continue only explicitly declared conservative fallback activities that the policy permits. If no such fallback is declared, return/record `blocked` and do not infer permission or select an activity. Apply any explicit interruption/expiry rule to an activity already running; absence of such a rule does not grant permission to continue. During CORE outage or fallback, WORLD dispatches no LLM/Tool work and creates no speech, cognition, intellectual achievement, or work-success result. After CORE returns, apply updated permissions only after authenticated LifePolicy and full WorkSet reconciliation. R05 has no persistent service or LifePolicy fallback behavior.

WORLD emits ordered, authorized Snapshot/Event/Observation reads. CORE initiates reads/streams and owns observation delivery, experience acceptance, and memory storage. WORLD does not call CORE back. PORTAL receives only an authorized CORE read-bridge projection and has no WORLD write credential. StoryPlayer controllers may use the same scoped Command interface; Renderer and PlaybackSession cannot write world facts.

Observation candidate, delivered context, CORE acceptance, and long-term memory are different records. A candidate names its Event evidence, observer, perception path/time, visibility rule, and permitted scope. Viewer delivery is computed as the authorized projection after a transaction minus the authorized projection before it; it is not a naive filter over global Event sequence numbers. `view_cursor` advances only for that scope. A permission/scope change, cursor loss, or invalid sequence requires a new Snapshot. Keep author view, Actor knowledge, and Viewer access separate; unset visibility never means public.

On ordinary restart, retain the Timeline epoch and replay the verified Snapshot/Event suffix. Restore from an earlier backup creates a new epoch and rejects old commands/projections until CORE has reconciled with a new full projection. Catch-up after downtime is bounded and deterministic; record any omitted interval as `recovery_gap`, never invent conversations, investigations, or work. Derived current time/freshness is not persisted in State hashes. Expiry changes that affect State become Events.

## 6. Target API and operations

These are provisional route proposals only, not endpoints available in R05 or adopted cross-module API contracts. Until CORE and WORLD owners agree, method/path, DTOs, authentication scope, versioning, error semantics, and projection receipts remain unfixed. The table is a design target for that review, not authority to implement an assumed shared contract.

| Purpose | Target route |
|---|---|
| Process health | `GET /health/live`, `GET /health/ready` |
| Validate / publish a Bundle | `POST /v1/worlds/{world}/revisions/validate`, `POST /v1/worlds/{world}/revisions/publish` |
| Create or fork a Timeline | `POST /v1/worlds/{world}/timelines` |
| Read a Snapshot / ordered history | `GET /v1/worlds/{world}/timelines/{timeline}/snapshot`, `GET /v1/worlds/{world}/timelines/{timeline}/events`, `GET /v1/worlds/{world}/timelines/{timeline}/stream` |
| Apply a world command / poll its result | `POST /v1/worlds/{world}/timelines/{timeline}/commands`, `GET /v1/worlds/{world}/timelines/{timeline}/receipts/{receipt}` |
| CORE projection | `PUT /v1/worlds/{world}/timelines/{timeline}/core-projections/{actor}/{kind}` |
| Confirmed speech / visits | `POST /v1/worlds/{world}/timelines/{timeline}/confirmed-utterances`, `POST /v1/worlds/{world}/timelines/{timeline}/visits` |

The target uses least-privilege owner credentials: CORE credentials cannot publish author content; author/publish authority cannot create CORE identity or memory; Viewer credentials are read-only. Browser clients never receive WORLD credentials, Store paths, raw Memory, or owner secrets. After route agreement, retain one canonical WORLD route and port as set by the authoritative runtime configuration. R05 defines no server port, so R07 is blocked until the existing runtime/port owner resolves that value. No alternate port, direct backend route, or mock-as-production fallback is allowed to make integration pass. An explicit-config source foundation exists in [R07](R07.md); it does not satisfy this block or claim the canonical route.

## 7. CLI, LLM, and Boundary split

| Class | WORLD responsibility | Failure result and evidence |
|---|---|---|
| CLI / deterministic code | Parse and validate schemas; resolve immutable Profile references; validate registered rule parameters; compute deterministic transitions, canonical digests, hashes, replay, cursors, limits, and migrations. | Bounded machine-readable result and nonzero status on invalid input; source revision, fixture/hash, config, OS, and output/receipt references. |
| LLM | No runtime Kernel role. An optional authoring aid may suggest settings or rule drafts; it cannot set canon, publish a Bundle, generate committed State, or call a world action. | Keep the proposal and model/input metadata separate; reject invalid schema/semantics; publication requires an authorized Boundary action. |
| Boundary | Authentication, capability/scope, exact identifiers, version and epoch, policy, body/transaction limits, CAS, idempotency, atomic persistence, read visibility, receipt, backup/restore, and external effects. | Fail closed with transport error or `rejected` Receipt as defined above; record protected audit evidence without exposing secrets. |

No model output or narrative text is runtime evidence. Every accepted external effect must have an owner-authenticated request, policy decision, durable WORLD transaction where applicable, and traceable Receipt/Event. This table is a design classification, not proof that those paths exist today.

## 8. Implementation stages and evidence

| Stage | WORLD-owned outcome | Completion evidence |
|---|---|---|
| R00/R01 prerequisites | Record current Repo/Owner/contracts and settle the ID compatibility and canonical route/port questions without replacing R05. | Source baseline, compatibility decision, route/port owner reference, R05 input/output regressions. |
| R03 geometry PoC prerequisite | Independently verify representative geometry/projection behavior. This PoC does not accept final art. | Reproducible geometry PoC artifact/fixture and independent result. No evidence is recorded here. |
| R06 EventStore | Add durable Event, State projection, Receipt, idempotency, single-writer transaction, Snapshot/replay, and crash recovery while preserving R05 inspect. SQLite WAL is a candidate pending dependency review. | AC-002/003/007/008/010/015 plus R05 regressions; crash injection, restart hash, duplicate/payload-conflict proof. |
| R07 Auth HTTP | Add authenticated readiness/read/write service and lifecycle around R05. | AC-016/017 and API negative cases through the canonical route and fixed port; unauthorized reads/writes, scope changes, buffer loss, Snapshot resync. |
| R08/R09 CORE bridge | Accept durable, ordered CORE-owned WorkSet and LifePolicy projections. CORE owns its publisher/outbox and schema adoption. | AC-004/005/011/012/013/014 with source epoch/revision, stale/empty/queued/running, restart/resync, and no duplicate external work. |
| R10/R11 life and space | Add deterministic non-cognitive life Activities, bounded recovery, measured SpatialBundle, navigation, occupancy, and reservations. | AC-006/008/009/024; exact reservation conflicts, safe interruption, privacy/room access, measured geometry and hashes. |
| R12/R13 PORTAL integration | Supply ordered read projection to CORE/PORTAL; keep PORTAL read-only, render two views from one state. | AC-016/017/023/024 through the canonical route; reconnect, redaction, same-browser audio dedupe, camera switch without State changes. |
| R14 ArtGate | Independent acceptance of final HOUSE art on required PC and mobile layouts. | Reference comparison and measured real-device evidence; geometry PoC is not this Gate. ArtGate remains required even if R06 starts earlier. |
| R15–R19 owner integrations | WORLD supplies facts, encounter opportunities, observation candidates, visit presence, and referenced events; CORE owns cognition, conversation, memory, and actual Actor registration; StoryPlayer owns narrative authoring/release. | AC-018–022/024, with each owner receipt and source evidence. WORLD alone cannot mark these integrations complete. |
| R20–R22 end-to-end and operation | Verify long-running use, outage/restart, backup/restore, deployment, and user-visible results. | AC-001–024 as applicable, native OS evidence, real authorized actors, fixed route, trace/receipt continuity, and R14 ArtGate result. |

The later `inputs/rencrow_final_20261019/docs/09_実装手順.md` permits a separate EventStore Implementation Unit after an independent geometry PoC. This document has no evidence that the PoC gate has passed; R06 must not be marked ready from this specification. The same plan keeps the ArtGate at R14. Design adoption, source implementation, deployment, readiness, and post-deployment acceptance are separate states.

### Platform and route acceptance

For each R06+ behavior contract, exercise native Go behavior on Ubuntu/Linux, Windows, and macOS; `build` or `vet` alone does not prove behavior on another OS. Keep protocol, config, health/readiness, error, and unavailable behavior equivalent. Integration and E2E evidence uses the canonical owner route and fixed port, real authentication/policy, the owner module, and an actual authorized user or CORE Agent. Do not substitute direct Store/function calls, test doubles, mock production routes, or alternate ports for that evidence. If a platform or route is not exercised, report that boundary as unverified.

### Acceptance ID mapping

The IDs are stable references to `contracts/acceptance_matrix.json` in the source bundle. Keep the matrix's `not_run` status until its actual scenario and required evidence are executed.

| ID | Acceptance area | Planned owner/stage |
|---|---|---|
| W14-AC-001 | Capability visibility, immutable published revision | WORLD, Bundle validation/publish |
| W14-AC-002 | Atomic Event/State/reservation/Receipt transaction | WORLD R06 |
| W14-AC-003 | Idempotent retry and body mismatch | WORLD R06 |
| W14-AC-004 | CORE projection order and source epoch | CORE + WORLD R08 |
| W14-AC-005 | Restore epoch rejection and CORE resync | WORLD + CORE R06/R08/R21 |
| W14-AC-006 | Real-time intent dependency revalidation | WORLD R10/R11 |
| W14-AC-007 | Strict edit/turn-based state CAS | WORLD R06 |
| W14-AC-008 | Atomic multi-resource reservation/release | WORLD R06/R11 |
| W14-AC-009 | Long Activity independent of real work movement | WORLD + CORE R10 |
| W14-AC-010 | Replay hash and derived clock/freshness split | WORLD R06/R20 |
| W14-AC-011 | Safe non-cognitive life during CORE outage/policy expiry | WORLD + CORE R09/R10 |
| W14-AC-012 | WorkSet queued/running/chat classification | CORE + WORLD R08 |
| W14-AC-013 | Full-set resync, missing terminal notice, stale vs empty | CORE + WORLD R08 |
| W14-AC-014 | Work result vs world presentation and external retry | CORE/Harness + WORLD R08/R10 |
| W14-AC-015 | Transaction-preserving pagination and bounds | WORLD R06/R07 |
| W14-AC-016 | Viewer before/after projection and private redaction | WORLD + CORE/PORTAL R07/R12 |
| W14-AC-017 | SSE reconnect, gaps, scope changes, Snapshot boundary | WORLD + CORE/PORTAL R07/R12/R13 |
| W14-AC-018 | Private-room observation vs delivery/experience acceptance | WORLD + CORE R11/R17 |
| W14-AC-019 | Fork ContextSnapshot and child knowledge isolation | WORLD references + CORE R15/R17 |
| W14-AC-020 | World/Timeline/Actor scope and canon/memory isolation | WORLD + CORE; StoryPlayer for releases |
| W14-AC-021 | Evidence-based diary/report and absent-Actor blocked result | CORE owns artifact; WORLD supplies evidence, R17 |
| W14-AC-022 | StoryRelease branch/canon and static/dynamic choices | StoryPlayer owns; WORLD supplies pinned references |
| W14-AC-023 | Main/PinP single-browser speech/audio dedupe | CORE/PORTAL owns, R13/R20 integration |
| W14-AC-024 | Eight HOUSE residents, Visitor, two views, art, long run | WORLD profile + CORE/PORTAL; R14/R18/R20 |

## 9. Unresolved decisions and blocked claims

- **ID compatibility:** the ASCII versus R05 Unicode-opaque conflict must be resolved before new cross-language schemas and API DTOs are frozen. Preserve exact source IDs meanwhile.
- **CORE adoption:** LifePolicy projection, authenticated principal/capability mapping, source epoch changes, and projection receipt ownership require CORE agreement. No WORLD-only spec can make these an implemented cross-module contract.
- **Runtime route/port:** R05 has no server port. R07 cannot start until the canonical port and route owner is identified from the existing runtime configuration.
- **R06 entry gate:** no independent geometry PoC evidence was found or produced in this documentation task. R06 readiness remains unproven; the R14 ArtGate remains a separate required milestone.
- **Store dependency:** SQLite WAL is only a candidate. R06 must confirm the existing WORLD durable-store boundary, supported Go driver, OS behavior, and backup/restore path before selecting a dependency.
- **HOUSE actor mapping:** Aka/Ao/Kin/Gin remain WORLD resident entities unless CORE separately registers identities and grants the needed authority. No identity or access grant is implied by R05 compatibility.
- **StoryPlayer and PORTAL:** their implementation, adoption, release, renderer, and playback acceptance remain with their owners. This document defines only WORLD interface boundaries.
- **Acceptance evidence:** all source-bundle W14 scenarios are `not_run`; this documentation change does not execute them or establish source, runtime, deployment, or live acceptance.

No migration or import is authorized by this design alone. Any future data conversion must preserve original hashes and IDs, record deterministic source-to-target mappings, report unresolved references, and keep validation, publication, CORE registration, and event creation as distinct outcomes.
