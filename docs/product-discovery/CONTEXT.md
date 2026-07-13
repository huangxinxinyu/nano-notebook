# Nano Notebook Product Context

Nano Notebook helps individual researchers and deep learners understand and develop knowledge from a bounded collection of sources. This glossary defines the product language used during discovery.

## Language

**Notebook**:
A persistent workspace for one research topic. It is the boundary that isolates sources and controls who can access them.
_Avoid_: Project, folder, knowledge base

**Source**:
An immutable snapshot of material intentionally added to a Notebook and used as the evidence base for understanding and producing work. It can be an uploaded document, pasted text, public web page, public YouTube video, uploaded audio, or uploaded image; its title can change, but its evidence content cannot.
_Avoid_: File, document, reference

**Chat**:
A private conversation between one Member and the assistant within a Notebook. A Member can create multiple independent Chats in the same Notebook; each has its own history and is visible only to its creator.
_Avoid_: Shared thread, discussion

**Grounded Answer**:
A Chat response whose factual claims are supported only by the Sources selected from the current Notebook. If the Sources are insufficient, the response states that limitation instead of silently adding model knowledge or web information.
_Avoid_: General answer, best-effort answer

**Research Agent**:
The read-only assistant operating inside a Chat. It can perform multiple retrieval, comparison, synthesis, and verification steps over selected Sources, but cannot modify the Notebook or interact with external systems.
_Avoid_: General agent, automation, chatbot

**Reasoning Trace**:
The user-visible, structured record of a Research Agent run, including its stages, retrievals, inspected evidence, concise selection rationale, comparisons, uncertainty, and conclusion summary. It explains observable research work without claiming to expose hidden model cognition.
_Avoid_: Raw chain of thought, thinking tokens

**Citation**:
An inline link from a key factual claim or synthesized conclusion in a Grounded Answer to its supporting Source evidence. It previews the original passage on hover and opens the passage in context when selected; very short Sources may be cited as a whole.
_Avoid_: Bibliography entry, unlinked source name

**Output**:
A durable, shared Notebook result derived from selected Sources, such as a report, study guide, mind map, quiz, slide deck, or audio overview. Outputs are a committed future product area but are not part of the initial release.
_Avoid_: Note, chat response, artifact

**Member**:
A user who has been granted access to a shared Notebook with one of its defined roles.
_Avoid_: Collaborator, participant

**Viewer**:
A Member role that can view a Notebook's Sources and conduct private Chats without adding, removing, or modifying Sources. The role follows Google NotebookLM's interaction-oriented Viewer model.
_Avoid_: Reader, guest

**Editor**:
A Member role that can view, add, and remove a Notebook's Sources in addition to conducting private Chats. It cannot manage access or delete the Notebook.
_Avoid_: Contributor

**Owner**:
The single Member responsible for a Notebook and its access. It has Editor capabilities and exclusively manages Members, changes roles, revokes access, transfers ownership, and deletes the Notebook.
_Avoid_: Admin, creator
