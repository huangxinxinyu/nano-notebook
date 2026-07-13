# Keep the core in Go behind Extractor Adapters

Nano Notebook's Control Plane, Durable Runtime, Worker coordination, permissions, Chat, and RAG orchestration will be implemented in Go. Source Workers may call replaceable Go libraries, external binaries, containerized Python processors, or model APIs, but those Extractor Adapters only produce a Normalized Source Artifact and never own authoritative state, permissions, Job lifecycle, or retrieval-index lifecycle.
