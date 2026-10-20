# WORLD identifier compatibility decision

Status: WORLD source decision for the R00/R01 identifier issue. The Go implementation and language-neutral fixtures are in `internal/idmap`; CORE and other adopters must run the same fixtures before their ASCII transport is considered compatible. This is not a claim that all v1.4 schemas are frozen.

## Canonical IDs

Canonical IDs remain exact valid UTF-8 opaque values, as in R05:

- nonempty;
- valid UTF-8;
- no surrounding whitespace;
- no control characters (`U+0000..U+001F`, `U+007F`);
- no trimming, case folding, Unicode normalization, or aliasing.

Interior spaces are allowed by the R05 rule. They are encoded by the transport below so an ASCII transport never has to carry a space.

## ASCII transport encoding

`internal/idmap` defines `utf8-base64url-tilde-v1`:

1. Validate the canonical ID.
2. If every byte is an RFC 3986 unreserved ASCII character (`A-Z`, `a-z`, `0-9`, `-`, `.`, `_`, `~`) and the first byte is not `~`, return it unchanged. This keeps path segments safe; reserved ASCII such as `/`, `?`, `#`, `%`, `:`, and spaces is encoded.
3. Otherwise return `"~" + base64url(UTF-8 bytes)` with padding removed (RFC 4648 URL-safe alphabet).

Decoding reverses this exactly. An encoded payload must be canonical unpadded base64url; padding, whitespace, and non-ASCII transport values are rejected.

Examples:

| Canonical | ASCII transport |
|---|---|
| `mio` | `mio` |
| `1f-living` | `1f-living` |
| `a~b` | `a~b` |
| `ミオ` | `~44Of44Kq` |
| `世界` | `~5LiW55WM` |
| `~mio` | `~fm1pbw` |
| `a b` | `~YSBi` |
| `a/b` | `~YS9i` |

Collision-freedom: an output starts with `~` exactly when it is encoded; a literal transport value never starts with `~` because every canonical ID with that first byte is encoded. Base64url is injective, and UTF-8 is used without normalization, so each transport value maps to exactly one canonical ID.

## Boundary rules

- **R05 JSON output and all canonical JSON**: exact UTF-8 IDs, unchanged.
- **R07 HTTP path segments** `{world}` and `{timeline}`: ASCII transport encoding. Clients encode a canonical ID with this mapping; the server decodes it before exact scope comparison. A raw non-ASCII or space-containing path value is `400 invalid_scope_id`, not an alias.
- **JSON request bodies and responses**: canonical UTF-8 IDs. JSON is already UTF-8; this mapping is not applied to every field.
- **Generated IDs** (`rcp_`, `txn_`, `evt_`): graphic ASCII and pass through unchanged.
- Any future transport with an ASCII-only constraint applies this mapping at that boundary. It must not normalize, lowercase, trim, or guess an alias.

## Fixtures and tests

`internal/idmap/testdata/cases.json` is the language-neutral fixture. `TestCrossLanguageFixtures` verifies every case in both directions, and additional tests reject invalid canonical IDs, noncanonical transport values, and Unicode normalization. A CORE or other-language adapter must consume the same fixture rather than reimplementing the algorithm from prose.

## Open boundaries

- CORE has not yet adopted this mapping or provided its own cross-language result.
- The mapping is applied to R07 path segments only; R06 persisted IDs and JSON DTOs remain canonical UTF-8.
- No maximum canonical ID length is imposed by this decision. Each owning contract must set and enforce its own bound.
- If CORE requires ASCII identifiers inside JSON rather than only on an ASCII transport, that is a separate schema decision and must use this mapping explicitly.
