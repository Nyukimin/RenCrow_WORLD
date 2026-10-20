# RenCrow_WORLD

RenCrow_WORLD owns the independent living-world state. The current implementation is the R05 foundation: a pure Go domain and explicit, one-shot input inspection. It is not a running service or a production WorldSnapshot source.

The only current implemented WORLD contract is [docs/R05.md](docs/R05.md). The adopted v1.4 design target for future WORLD work is [docs/implementation-spec-v1.4.md](docs/implementation-spec-v1.4.md); it is not a current API or running service. R06+ stages there cover durable events and state, authenticated HTTP, CORE projections, and later activity and spatial work. The R04 mock bridge in CORE and PORTAL is a separate proof of concept.

This repository has no deployed WORLD runtime, persistent store, listener, or production connection to CORE or PORTAL.
