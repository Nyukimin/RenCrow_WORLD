# RenCrow_WORLD

RenCrow_WORLD owns the independent living-world state. The current implementation is the R05 foundation: a pure Go domain and explicit, one-shot input inspection. It is not a running service or a production WorldSnapshot source.

The adopted R05 contract is [docs/R05.md](docs/R05.md). Later work packages own durable events and state (R06), HTTP and authentication (R07), and autonomous activity (R10). The R04 mock bridge in CORE and PORTAL is a separate proof of concept.

This repository has no deployed WORLD runtime, persistent store, listener, or production connection to CORE or PORTAL.
