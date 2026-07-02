# CLAUDE.md

Project: `cyclecount` — an AVR instruction cycle-counting / ISA analysis tool (Go).

## Fact verification rule (mandatory)

Do **not** answer AVR facts from memory. For any claim about an AVR
instruction, register, interrupt vector, pinout, cycle count, opcode encoding,
or device-specific behavior:

1. Confirm it against a primary source **before** stating it:
   - `avr-datasheet` MCP — full-text search over the Microchip datasheet PDFs
     (`search_documents`, `get_document_page`, `search_register`,
     `find_interrupt_vectors`, `find_pinout`).
   - `microchip` MCP — official Microchip product docs/specs.
   - The AVR Instruction Set Manual (DS40002198C) is the authority for ISA
     accuracy — see recent commits.
2. Cite the source: document + page (or register/section). If you can't cite
   it, say it's unverified rather than asserting it.
3. Same rule for third-party libraries/frameworks: check `context7` MCP rather
   than relying on training data.

If a lookup tool returns nothing, say so explicitly instead of filling the gap
from memory.
