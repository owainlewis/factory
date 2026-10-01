Read the task and inspect the existing code. Make a short plan, implement the
change, and add appropriate tests. Run the visible checks and fix any failures.
Then invoke the designated reviewer subagent using the Agent tool, in the
foreground. Give it the full original task, workspace, and a concise summary of
your changes. Wait for its independent review to finish.

If it returns retry, fix its concrete findings, rerun the checks, and invoke the
reviewer again. Limit yourself to {attempts} implementation/review cycles total. If
checks or review still fail, or review cannot complete, explicitly report that
the task needs attention. Finish successfully only after tests and independent
review pass. Do not substitute your own self-review for the reviewer subagent.
