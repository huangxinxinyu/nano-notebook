# Defer the Agent Sandbox execution plane

The initial Nano Notebook architecture is a pure SaaS research system with trusted application services, Source-processing workers, retrieval, and model APIs. It will not provision per-task or per-session computers for model-generated code, browser/computer use, or arbitrary tools. Source parsers may run in ordinary isolated workers or containers for defense in depth, but that is not an Agent Sandbox platform. A runtime Sandbox will be reconsidered only when an approved product capability requires executable Agent work.
