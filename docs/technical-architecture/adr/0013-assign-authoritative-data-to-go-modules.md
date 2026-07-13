# Assign authoritative data to explicit Go Modules

The modular monolith will separate Identity, Notebook, Source, Chat, Agent, Retrieval, Jobs, and Models ownership behind in-process interfaces while sharing one PostgreSQL instance. Notebook alone owns roles, Source owns canonical citable content, Chat owns private history and published answers, Agent owns Runs and drafts, Retrieval owns rebuildable indexes, Jobs owns delivery mechanics, and Models owns Bifrost integration; cross-Module table access and reverse dependencies are prohibited even though network services are not introduced.
