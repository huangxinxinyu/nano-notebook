# Enforce authorization in Go and PostgreSQL RLS

Go Capability policies will decide Viewer, Editor, and Owner operations, while PostgreSQL row-level security provides defense in depth for Notebook-visible data and creator-private Chat, Message, and Agent Run rows. Request transactions carry a verified Principal through transaction-local database context, Workers reauthorize at durable boundaries, Qdrant filters are server-constructed from the Run Evidence Set, and narrowly scoped maintenance roles are separated from user-facing connections; an external policy service is not introduced.
