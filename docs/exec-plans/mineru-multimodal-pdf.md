# MinerU Multimodal PDF Parsing Execution Plan

## Goal

Make MinerU a robust explicit parser engine for multimodal PDF ingestion while
reusing WeKnora's existing image storage, chunking, indexing, and async VLM OCR
and caption pipeline.

## Current Round

- Harden self-hosted MinerU response parsing across common response shapes.
- Fail empty MinerU parse results instead of silently indexing empty documents.
- Keep MinerU and MinerU Cloud response logs structure-only and redact document
  text, markdown, image payloads, and secret-like fields.
- Record parser metadata on MinerU read results.
- Document the runtime flow, configuration, safety constraints, and verification
  checklist.

## Progress

- Completed: self-hosted `mineru` and `mineru_cloud` remain explicit parser
  engines with no default PDF behavior change.
- Completed: self-hosted MinerU supports `results.document`,
  `results.files`, and `results.<filename>` response shapes.
- Completed: empty MinerU results become parse errors.
- Completed: unit coverage added for missing config, response extraction, empty
  results, markdown preservation, and image path matching.
- Remaining: live end-to-end verification against a running MinerU service and
  configured VLM/storage environment.

## Validation

- Passed: `git diff --check`.
- Not run in this environment: `gofmt` and targeted `go test`; the local shell
  cannot find `go` or `gofmt`.
- For live validation, configure `mineru_endpoint`, set PDF
  `parser_engine_rules` to `mineru`, upload a scanned PDF, and confirm markdown,
  stored images, multimodal child chunks, and retrieval enrichment.
