# RenCrow_WORLD

RenCrow_WORLD owns the independent living-world state. The current implementation is the R05 one-shot inspect command plus the R06 durable EventStore library (a pure Go domain, explicit input inspection, and an on-disk event/state store with no listener). It is not a running service or a production WorldSnapshot source.

The implemented WORLD contracts are [docs/R05.md](docs/R05.md) for the one-shot inspect command and [docs/R06.md](docs/R06.md) for the durable EventStore library. The adopted v1.4 design target for future WORLD work is [docs/implementation-spec-v1.4.md](docs/implementation-spec-v1.4.md); it is not a current API or running service. R06+ stages there cover durable events and state, authenticated HTTP, CORE projections, and later activity and spatial work. The R04 mock bridge in CORE and PORTAL is a separate proof of concept.

This repository has no deployed WORLD runtime, listener, or production connection to CORE or PORTAL. The R06 store is a library with a single logical writer; it is not an operational service.
