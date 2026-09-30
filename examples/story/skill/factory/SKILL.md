---
name: factory
description: Takes a GitHub issue to a reviewed pull request, with no human in the loop. Use when asked to build an issue end to end.
---

# Factory

Take the provided GitHub issue to a pull request. Work on a new branch from the
latest default branch. Do not ask the user anything.

1. Review the provided issue and write a PRODUCT_SPEC.md and a TECHNICAL_SPEC.md.
2. Implement TECHNICAL_SPEC.md, with a test for each requirement in PRODUCT_SPEC.md.
3. Run the tests and fix any failures.
4. Open a pull request that closes the issue.
5. Wait for CI with `gh pr checks --watch`. Fix any failures and push.
6. Wait up to 10 minutes for automated reviews. Fix each comment, or reply with
   why not. If you pushed a fix, go back to step 5.
7. Comment on the issue with the pull request link and what happened.

Stop after 3 rounds of fixes. Never weaken a test, merge, or force-push.
