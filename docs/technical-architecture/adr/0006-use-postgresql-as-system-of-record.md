# Use PostgreSQL as the structured System of Record

Nano Notebook will keep users, Notebook membership, Source metadata, Chats, Messages, Agent Runs, durable Jobs, citations, and execution records in one PostgreSQL database so permission and lifecycle changes can be transactional. S3 owns binary objects, while retrieval and observability stores are derived and rebuildable rather than authoritative; the initial system will not split databases by Module or adopt full event sourcing.
