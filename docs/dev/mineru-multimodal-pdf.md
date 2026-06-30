# MinerU Multimodal PDF Parsing

This document describes how WeKnora integrates MinerU as an explicit parser
engine for multimodal PDF ingestion.

## Runtime Flow

1. Configure a tenant-level parser engine:
   - Self-hosted MinerU: `mineru_endpoint`
   - MinerU Cloud: `mineru_api_key`
2. Select the parser in a knowledge base or per-upload process override:

```json
{
  "parser_engine_rules": [
    {
      "file_types": ["pdf"],
      "engine": "mineru"
    }
  ]
}
```

3. The selected reader returns a unified `ReadResult`:
   - `MarkdownContent`: MinerU markdown with headings, formulas, tables, and image references preserved.
   - `ImageRefs`: extracted image bytes keyed by the original markdown or HTML reference.
   - `Metadata`: parser engine and backend/model information.
4. `ImageResolver.ResolveAndStore` uploads extracted images to the configured storage provider and rewrites markdown image URLs.
5. The normal knowledge pipeline chunks and indexes the markdown, then queues `image:multimodal` tasks to generate image OCR and captions.
6. Retrieval merges text, image OCR, and image captions through the existing chunk and enrichment logic.

## Self-Hosted MinerU

Run MinerU as a separate internal service. Do not embed model weights in the
WeKnora app or docreader containers.

Recommended tenant parser config:

```json
{
  "mineru_endpoint": "http://mineru:8000",
  "mineru_model": "pipeline",
  "mineru_enable_formula": true,
  "mineru_enable_table": true,
  "mineru_enable_ocr": true,
  "mineru_language": "ch"
}
```

For complex layouts, charts, and visually dense PDFs, use a VLM or hybrid
MinerU backend and provide the vLLM server URL:

```json
{
  "mineru_endpoint": "http://mineru:8000",
  "mineru_model": "hybrid-http-client",
  "mineru_vlm_server_url": "http://vllm:8000",
  "mineru_enable_formula": true,
  "mineru_enable_table": true,
  "mineru_enable_ocr": true
}
```

## MinerU Cloud

MinerU Cloud is selected with `engine: "mineru_cloud"` and configured with:

```json
{
  "mineru_api_key": "YOUR_API_KEY",
  "mineru_cloud_model": "pipeline",
  "mineru_cloud_enable_formula": true,
  "mineru_cloud_enable_table": true,
  "mineru_cloud_enable_ocr": true,
  "mineru_cloud_language": "ch"
}
```

API keys are stored in the existing tenant parser-engine config path. Do not put
real keys in `.env.example`, docs, screenshots, or logs.

## Behavior And Failure Modes

- WeKnora does not change the default PDF parser automatically. PDFs use MinerU only when the knowledge base or upload override explicitly selects `mineru` or `mineru_cloud`.
- Self-hosted MinerU uses the synchronous `/file_parse` API in this version. Large-document async `/tasks` support can be added later without changing upload APIs.
- Empty MinerU responses fail the docreader stage instead of creating empty knowledge.
- MinerU response logs are structure-only; document text, markdown, token-like fields, and image payloads are redacted.
- If a stable internal MinerU image host must be preserved instead of rewritten to object storage, configure `IMAGE_HOST_KEEP_URL`.

## Verification Checklist

- `GET /api/v1/system/parser-engines` lists `mineru` and `mineru_cloud`.
- `POST /api/v1/system/parser-engines/check` reports configured MinerU availability.
- A PDF knowledge base with `parser_engine_rules` set to `mineru` parses markdown and persists images.
- Scanned PDFs with stored page images enqueue multimodal tasks and produce `image_ocr` / `image_caption` chunks when VLM is enabled.
- MinerU service errors, timeouts, non-200 responses, and empty results are visible as docreader-stage failures.
