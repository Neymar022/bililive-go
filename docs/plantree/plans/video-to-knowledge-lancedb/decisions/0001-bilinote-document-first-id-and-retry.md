# Decision 0001: BiliNote Document-First ID And Retry Strategy

日期：2026-05-28

## Status

Accepted for the next BiliNote-scoped patch. This decision does not make the Bililive-go side implementation-ready yet.

## Context

Task `bililive-go-473` proved the BiliNote `generate_note=true` + `non_blocking=true` request returns immediately and generates visible Markdown, but the background knowledge write failed with `UNIQUE constraint failed: knowledge_items.id`.

Read-only reproduction against the production BiliNote container and failed note record showed:

- Document-first extraction produced `drafts=11`, `accepted=11`, but only `unique_accepted_ids=9`.
- Markdown headings without timestamps fell back to `start=0.0` and the same 180-second evidence window ending at `181.64`.
- Numbered headings such as `1. ...` and `2. ...` collapsed through `_build_abstract` into `装修: 1` and `装修: 2`.
- `deterministic_knowledge_id` used only `source_type`, `source_id`, `start`, `end`, and `l0_abstract`.
- `KnowledgeStore.upsert_drafts` accepted duplicate draft IDs in the same batch and only failed at commit time.

The local BiliNote main checkout is stale for this feature; the relevant code baseline is the PR #40 style worktree at `/Volumes/ISCSI-Disk/Folder/Bililive-go/.worktrees/bilinote-unified-knowledge-memory` and the production container code.

## Decision

Use a three-part BiliNote fix:

1. Document-first draft IDs must include section-level identity.
   - Add a section index and normalized heading, or a stable section content hash, to the document-first ID seed.
   - Keep transcript-first IDs compatible unless a separate migration decision is made.
2. Document-first abstracts must not collapse numbered headings.
   - Strip or ignore leading heading ordinals like `1.`, `2.`, `1、`, or `2)` before building the abstract, or build the abstract from normalized heading plus body text without splitting on the ordinal dot.
3. `KnowledgeStore.upsert_drafts` must defensively dedupe accepted drafts by `draft.id` before embedding and DB writes.
   - Keep the first draft for a duplicate ID.
   - Report later duplicates as non-fatal duplicate-batch rejections or audit entries.
   - Do not invent suffix IDs at the store layer; if different document sections collide, the extractor ID seed is the bug to fix.

## Failed Task 473 Handling

After the BiliNote patch is deployed, retry task `bililive-go-473` with the same real payload, same source identity, and same `task_id`, using `generate_note=true` and `non_blocking=true`.

Do not use current `backfill_notes` or `source rebuild` for this failed record:

- `backfill_notes` reconstructs a request from the note record but currently calls the extractor without the saved Markdown, so it would fall back to transcript-first behavior.
- `source rebuild` requires an existing `KnowledgeSource`; task `473` has no `knowledge_sources` or `knowledge_items` row because the failed transaction never committed.

No DB cleanup is needed before the retry because source/items were not created. The retry may overwrite the failed note record status through the existing `create_or_update_record` path. If a future partial source exists, use a force/delete rebuild path only after verifying the source identity and old item count.

## Required Tests

- Extractor regression: two Markdown sections with repeated numeric prefixes and no timestamps must produce unique document-first draft IDs while preserving `原字幕证据`.
- Store regression: two accepted drafts with the same `draft.id` in one batch must not raise an integrity error; only unique IDs should reach embedding/index rows.
- Route regression: `generate_note=true` document-first ingest using duplicate-looking numbered Markdown should persist a source and items, not fail in background or sync mode.

## Verification

After deployment, verify in this order:

1. BiliNote backend tests for the extractor/store/route regressions pass.
2. NAS `/api/knowledge/runtime/machine` reports the patched image SHA and configured ingest/vector state.
3. Re-run task `473` once with the same real payload and `non_blocking=true`; confirm the initial HTTP response is queued.
4. Confirm the note record reaches success or a non-failed terminal state.
5. Confirm `knowledge_sources` and `knowledge_items` exist for the task/source.
6. Confirm successful items include document-first content, `原字幕证据`, `source_video_path`, `subtitle_path`, `start`, and `end`.
7. Confirm LanceDB health and search/index visibility for the sample.

## Consequences

- This keeps BiliNote responsible for knowledge identity and dedupe.
- Bililive-go remains blocked from automatic knowledge-sync implementation until the BiliNote patch is deployed and the real sample verifies source/items and LanceDB search.
- Reusing the same task/source identity keeps the production evidence path simple and avoids leaving a second successful task beside the failed `bililive-go-473` record.
