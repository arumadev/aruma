# Firenook: a framework-level look (overview only)

- Repo: https://github.com/firenook-dev/firenook, commit `24040f782be6f30c37b0e3d9d13375bf7f6d2cea` (2026-10-20), Apache-2.0 with NOTICE. Rust, about 386k lines incl. console.
- What it is: a drop-in replacement for the **Firebase Local Emulator Suite** (local development, not production): Firestore, Auth, Storage, Functions, Pub/Sub, Tasks, console. **Realtime Database is not implemented** (README.md:106, COMPATIBILITY.md:16).
- Read: README.md, DESIGN.md (sections Components, Persistence, Browser transport, Service selection, HTTP fronts), COMPATIBILITY.md, conformance/README.md. Nothing copied; the owner asked for an overview only.

## Ideas at the framework level

| Idea | Where | Use for Aruma RTDB |
|---|---|---|
| One shared engine (store, query, watch broker), with each SDK transport as a thin adapter ("fronts": grpc, rest, webchannel) | DESIGN.md "Components" | Same shape as the plan: `wire` and `rest` adapters over one `core` |
| Rules split into an expression engine and a runtime that builds the request context | DESIGN.md "Components" (`rules-engine`, `rules-runtime`) | Split the Go rules package the same way: parser/evaluator vs RTDB context (auth, data, newData, root, now) |
| Two independent oracles: captures from Google's emulator and captures from production; when they differ, the test names which one it follows | DESIGN.md "Browser transport" | Exactly our B-track golden frames plus the owner-approved real-Firebase runs; record the target per fixture |
| Fixtures record the official tool version, SDK versions and SHA-256 of the bytes | conformance/README.md | Do the same for RTDB frame captures (emulator 4.11.2, SDK commits) |
| Length prefixes counted in UTF-16 code units, not UTF-8 bytes | DESIGN.md "Browser transport" | RTDB frame limit (16384) is a JS string length, i.e. UTF-16 units; split on character boundaries (Lark learned this too) |
| Nagle disabled on every HTTP front; a measured 40 ms stall on reused keep-alive connections | DESIGN.md "HTTP fronts and Nagle's algorithm" | Go sets TCP_NODELAY by default on TCP conns; verify for hijacked WebSocket conns, nothing to do otherwise |
| Write-behind durability: ack before fsync, flush once a second | DESIGN.md "Write-behind durability" | Fine for an emulator, **not** for Aruma: a production server must not ack a write that a crash can lose |
| Scaling regression with a fixed synthetic dataset and fixed latency/RSS bounds on Linux | DESIGN.md "Persistence and memory" | Same idea for the RTDB load test: fixed seed, fixed bounds, measured in CI |

## Limits of this look
- No RTDB code exists in Firenook, so it says nothing about the RTDB wire protocol, ordering or rules.
- The owner mentioned experiments of building RTDB with Firenook; they were not provided, so they are not covered here.
