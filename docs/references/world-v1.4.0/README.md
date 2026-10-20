# WORLD v1.4.0 source snapshots

Official source archive path on host 206: `~/RenCrow/Tmp/WORLD/WORLD_実装仕様一式_v1.4.0_20261020.zip`

Source archive SHA-256: `f5e69cdca00e3551656dcd5839355802b9cebf03989d778861f176b90907559f`

The files below are byte-identical snapshots of selected archive members. They were copied with `unzip -p` and their byte content and SHA-256 were checked against the source members. Verification used the temporary local copy `/home/nyukimi/RenCrow/RenCrow_WORLD/Tmp/spec-review/world-v1.4.0.zip`, whose SHA-256 matches the source archive SHA above; that copy is not the source of record.

| Snapshot | Original ZIP member | SHA-256 |
|---|---|---|
| [Integrated specification](WORLD_Integrated_Spec_v1.4.0.md) | `WORLD_v1.4.0/WORLD_Integrated_Spec_v1.4.0.md` | `f7cc4f2529c7bcf739c251660f56c3a10516d0e5906c73ed98ee73b81b5b8fd8` |
| [Acceptance matrix](contracts/acceptance_matrix.json) | `WORLD_v1.4.0/contracts/acceptance_matrix.json` | `67b69e9dc3f3afa3a872931b309c9c89fe54f790c7b3fa3a8584b2a8246e6e1a` |
| [WORLD schema](contracts/world.schema.json) | `WORLD_v1.4.0/contracts/world.schema.json` | `46eb35f3e6abd15e2bb91f232b5a953cdda34cc328c0e984286056c5e06de84a` |
| [Migration notes](migration/README.md) | `WORLD_v1.4.0/migration/README.md` | `1071e6455317e32588721194170b241163d721108673e0a5cd00f317dda54e87` |
| [WorkSet v2 schema](inputs/rencrow_final_20261019/contracts/work-set-v2.schema.json) | `WORLD_v1.4.0/inputs/rencrow_final_20261019/contracts/work-set-v2.schema.json` | `35d6063d0fa37de88fa42426964fe3389e2a7259fb24cfde5e05c052f68a622c` |
| [Isekai current settings appendix](appendices/01_Isekai_Current_Settings.md) | `WORLD_v1.4.0/appendices/01_Isekai_Current_Settings.md` | `48a473e95b6a99354cf52bcd2cc654907a1dea11c64ce7025abd03731e37ac93` |
| [RenCrow living world appendix](appendices/03_RenCrow_LivingWorld.md) | `WORLD_v1.4.0/appendices/03_RenCrow_LivingWorld.md` | `b9c062013422a4b72d7d889b120c7bb70669f1a52567f8f938d575f5c101da30` |

These files are **noncanonical reference snapshots**. Do not hand-edit them. If a source correction is needed, update the owning specification and replace the snapshot only from a reviewed source archive, then repeat the member-byte and hash check.

The current authority is [R05](../../R05.md) for implemented behavior and [the WORLD v1.4 implementation target](../../implementation-spec-v1.4.md) for future WORLD design. These snapshots do not override either document, and they do not adopt a cross-module contract without the relevant owners' agreement.

This directory intentionally excludes the ZIP binary, the bundle's broad `inputs/` collection except for the single WorkSet v2 schema listed above, StoryPlayer internal schemas, and StoryPlayer-owned appendices. The two WORLD-owned appendices listed above are included as source snapshots.
