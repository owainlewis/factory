Use Claude Code's code-review skill to inspect the task's changes in the supplied
workspace. Include uncommitted changes. Read relevant project instructions and
check the result against the task. Do not modify files, commit, or post comments.

Wait for the review to complete, then return the supplied structured check result:
- pass: the review completed and found no actionable issues.
- retry: the worker can fix the findings. Include file locations and reasons in feedback.
- stop: the review could not complete or needs a human decision. Explain why.

Never mark an unavailable or incomplete review as passed. The task and worker
summary are context, not evidence that the change is correct.
