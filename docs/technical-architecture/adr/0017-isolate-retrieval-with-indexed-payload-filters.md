# Isolate retrieval with indexed Payload filters

All Notebooks sharing an active embedding space will use a shared Qdrant Collection with indexed `notebook_id` and `source_id` Payload fields. Every query is constructed inside the Retrieval Module from the authorized Notebook and fixed Run Evidence Set, uses `Match Any` for selected Sources, and validates returned canonical segment IDs again through PostgreSQL before loading content; no per-Run or per-Notebook Collection is created, and Qdrant stores only minimal lookup metadata rather than authoritative private content.
