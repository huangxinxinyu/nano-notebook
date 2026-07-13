# Use a modular Go control plane with independent Workers

Nano Notebook will begin as one Go codebase whose Control Plane and Workers are separate processes and deployment units. Product ownership boundaries remain Modules with in-process interfaces rather than internal HTTP or gRPC services. Long-running Source ingestion, Agent runs, and evaluations execute only as durable Jobs in Workers. A Module may be extracted later when measured scaling, failure-isolation, or ownership needs justify the operational cost.
