# Jules PR prompt part - inline review comments

Paste the block below at the top of a Jules session prompt for PR work.
Fill in OWNER, REPO and PR_NUMBER first. Jules runs in a VM with the
repo cloned, so all paths below are repo-relative.

## Paste this into Jules

> For PR OWNER/REPO number PR_NUMBER: before starting, fetch inline
> review comments with gh and treat every unresolved CodeRabbit or
> human finding as in-scope. Address each one in this session. Do not
> mark them done without a code change or a reply explaining why no
> change was needed.
>
> Run:
>
> gh api "repos/OWNER/REPO/pulls/PR_NUMBER/comments" --jq '.[] | "\(.path):\(.line) [@\(.user.login)] \(.body)"'
>
> Then also fetch review-level comments:
>
> gh api "repos/OWNER/REPO/pulls/PR_NUMBER/reviews" --jq '.[] | "\(.id) [@\(.user.login)] state=\(.state) \(.body)"'
>
> Rules:
> - Inline comments endpoint first, reviews endpoint second. Replies can
>   live on the reviews endpoint, so check both.
> - Every comment gives path, line, author and full body. Use that as
>   the fix list.
> - Keep the CodeRabbit summary off. Do not enable top-level posting of
>   findings as a substitute. The API fetch above is the reliable source.
> - Repo conventions still apply: branch off develop, PRs target
>   develop, one logical change per PR, verify with go vet and tests
>   where the task touches code.

## Notes for the person assigning the task

- The comments endpoint covers file, line, author and body. That is
  what found the original 7 findings.
- The reviews endpoint is messier (nested discussion resolution) but
  needed for replies. For read-the-suggestions purposes the comments
  endpoint covers most of it.
- If Jules works from a GitHub issue rather than a PR, copy the inline
  findings into the issue body or task description when assigning.
  Then no API knowledge is needed at all.
- Do not put secrets in the prompt. The gh commands above need only
  the repo read scope Jules already has.
