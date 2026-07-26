# PR #12 Review Follow-up

## Deferred: duplicate CoinGecko provider adapters

`lookupProvider` and `enrichProvider` in `internal/marketdata/service.go`
implement the same 17-method `marketpipeline.Provider` interface. Fifteen
method implementations are effectively identical, `Logf` differs only by its
prefix, and `Apply` is the only materially different method.

This is genuine duplication and creates drift risk when cache, store, fetch,
miss-cache, or record metadata behavior changes. It is not a correctness issue
and is deferred from PR #12 so the distinct comparison and enrichment
definitions of a usable record remain explicit. A later cleanup should share
the common adapter implementation while keeping the two `Apply` behaviors at
the existing `marketpipeline.Provider` seam.

## Resolved: provider timeout preserves completed batch results

The comparison module now retains an immutable progress snapshot whenever a
cache tier or provider batch applies fresh results. A fetched batch publishes
its snapshot before starting PostgreSQL or Redis persistence, so a five-second
deadline that expires during a write still returns the completed provider data.

Snapshots are deep-copied across the provider/comparison seam and again when
the response takes ownership. Publications after cancellation are ignored, so
late work cannot mutate emitted JSON. The timeout cancels unfinished work;
pipeline checks prevent later tiers, batches, or writes from starting, while an
already-active context-aware operation may continue briefly only as it observes
cancellation and unwinds.

Regression tests cover a deadline during active persistence, preservation of
the pre-persistence snapshot, cancellation of the worker, suppression of the
subsequent Redis write, and isolation from late provider mutation.

## Addressed in this follow-up

- Remove the unused `matchingRecord` and `incompleteKeys` helpers left behind
  by the shared-pipeline refactor.
- Make the CoinGecko and CoinMarketCap resolvers return a complete
  `marketpipeline.Request` instead of making callers unpack and immediately
  reconstruct `ExpectedID`, `IndexesByKey`, and `KeyOrder`.
