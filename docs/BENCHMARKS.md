# Benchmarks

## Editing a 1 GiB file: whole-file (v1) vs. chunked (v2) sync

The Phase 6 headline. One device edits a 1 GiB file of random (incompressible) data and the change syncs to a second device: upload on A, download on B. Both run the real sync engine against the real server (SQLite, disk storage, HTTP over loopback). **Bytes** are HTTP payload bytes on the wire, counted by a wrapping `http.RoundTripper`. **Time** is wall-clock time for the whole sync cycle: folder scan, hashing, chunking, transfer and commit.

| Change to a 1024 MiB file | Upload v1 (whole file) | Upload v2 (chunks) | Download v1 | Download v2 | Bytes saved |
|---|---|---|---|---|---|
| initial upload | 1024.0 MiB · 7.9s | 1024.1 MiB · 8.3s | 1024.0 MiB · 2.9s | 1024.0 MiB · 3.5s | 0% |
| overwrite 1 MiB in the middle | 1024.0 MiB · 7.6s | **4.5 MiB** · 3.8s | 1024.0 MiB · 3.5s | **4.5 MiB** · 6.9s | **99.6%** |
| insert 4 KiB near the start | 1024.0 MiB · 8.0s | **2.3 MiB** · 4.2s | 1024.0 MiB · 3.4s | **2.3 MiB** · 6.6s | **99.8%** |
| append 10 MiB | 1034.0 MiB · 7.5s | **10.8 MiB** · 3.9s | 1034.0 MiB · 3.4s | **10.8 MiB** · 7.1s | **99.0%** |

The same transfers at **100 Mbit/s**, a typical home upload link (wire time only, computed from the byte counts):

| Change | v1 upload | v2 upload |
|---|---|---|
| initial upload | 1.4 min | 1.4 min |
| overwrite 1 MiB in the middle | 1.4 min | **0.4 s** |
| insert 4 KiB near the start | 1.4 min | **0.2 s** |
| append 10 MiB | 1.4 min | **0.9 s** |

### Reading the numbers

- **The insertion row is the one that matters.** Inserting 4 KiB shifts every following byte. With fixed-size blocks, every block after the insertion would change: about 1 GiB re-sent, just like v1. Content-defined chunking finds the same boundaries again right after the edit, so only the chunk containing the edit (≈ 2.3 MiB) travels. `TestFixedSizeComparison` in `shared/chunker` shows the same effect on 32 MiB: 0 fixed-size blocks unchanged vs. 15 of 16 CDC chunks unchanged.
- **Overwrite** touches the chunk(s) containing the edited range. A 1 MiB write can straddle a boundary, hence 2 chunks (4.5 MiB).
- **Append** sends the new bytes plus the old last chunk, whose boundary moved.
- **Initial upload** costs the same either way. Chunking adds about 100 KB of check/commit JSON and a few percent of CPU.

### Why v2 downloads are slower on loopback, and why that doesn't matter on a real network

On loopback, bandwidth is effectively unlimited (several GB/s), so the v1 download is just a 1 GiB memory copy (≈ 3 s). The v2 download instead:

1. reads and re-chunks the old local copy (1 GiB of SHA-256, ≈ 2.2 s on this CPU) to find reusable chunks,
2. copies the reused chunks into the new file,
3. verifies the assembled file's SHA-256 end to end (another 1 GiB read).

That is CPU work traded for bandwidth. At 100 Mbit/s the v1 download takes **86 s** of wire time; v2 needs **0.4 s** of wire time plus about 7 s of local hashing. The upload side has the same trade-off but it pays off even on loopback (7.6 s → 3.8 s), because the server no longer ingests 1 GiB.

Two optimizations would remove most of the local cost, and were listed as follow-ups in the Phase 6 handoff:
- Keep each synced file's manifest in the local index, so an unchanged local copy (same size and mtime as its base) doesn't have to be re-chunked.
- Skip the final whole-file hash when every chunk was verified individually (it's kept for now as defence in depth).

### Setup

- Intel Xeon @ 2.10 GHz, **2 vCPU**, 8 GB RAM, Linux, Go 1.24.7, ext4 on virtio disk; client and server on the same machine.
- Chunking: FastCDC, 512 KiB min / 2 MiB avg / 8 MiB max (a 1 GiB file is roughly 400–500 chunks). 4 parallel chunk requests.
- File: 1 GiB of pseudo-random bytes (PCG). Real data compresses and dedups better; random data is the worst case for dedup and makes the numbers depend only on the chunking.

Reproduce (needs about 7 GB free disk, takes about 2 minutes):

```bash
NULLDRIVE_BENCH=1 go test ./server/internal/synctest -run BenchmarkLargeFile -v -timeout 30m
# smaller/faster: NULLDRIVE_BENCH_MB=64 …   write the tables to a file: NULLDRIVE_BENCH_OUT=out.md …
```

## Chunker throughput

`go test ./shared/chunker -bench .` on the same 2-vCPU machine, 64 MiB input:

| | Throughput |
|---|---|
| Boundary detection only (Gear hash, `BenchmarkCutOnly`) | ≈ 1.2–1.4 GB/s |
| Chunking + SHA-256 of every chunk (`BenchmarkChunker`) | ≈ 470–560 MB/s |

SHA-256 dominates. The rolling hash is one shift, one add and one table lookup per byte, with no hashing at all in the first 512 KiB of every chunk (FastCDC cut-point skipping).

## Chunk-size distribution and stability

From `go test ./shared/chunker -v` (64 MiB random input):

- 26 chunks, average 2.46 MiB. Every chunk falls within [512 KiB, 8 MiB].
- After each edit, 28 of 29 chunks are unchanged: insert 1 byte at the start (0.60 MiB to upload), insert 100 bytes mid-file (1.60 MiB), delete 4 KiB (2.62 MiB), overwrite 1 KiB (3.74 MiB), append 1 MiB (1.07 MiB).
