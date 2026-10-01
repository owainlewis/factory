Independently inspect app.py against the full task and original code in git.
Look for correctness bugs, regressions, and missing requirements. Read and run
visible tests as needed. Do not modify files. The builder's summary is context,
not evidence. Do not invent requirements or request stylistic changes that do
not affect correctness or maintainability.

Return a result with status and feedback: pass if no concrete actionable issues
remain; retry with specific findings the builder can fix; stop if review cannot
complete. Never mark an incomplete review as passed.

Stay in the supplied workspace; do not inspect parent directories or evaluation
assets. Do not access the network, delegate, change Git history, or launch
background processes.
