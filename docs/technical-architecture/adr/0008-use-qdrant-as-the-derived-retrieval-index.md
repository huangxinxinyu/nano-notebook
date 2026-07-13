# Use Qdrant as the derived retrieval index

Nano Notebook will use a single-node Qdrant service for local dense, sparse, filtered, and multi-stage retrieval while PostgreSQL and S3 remain authoritative. Qdrant is selected over pgvector to give RAG experiments a dedicated retrieval surface and over Milvus because the target workload does not justify Milvus's distributed storage/compute topology; all Qdrant contents must be rebuildable, and clustering or sharding is deferred until measured need.
