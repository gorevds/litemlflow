# LiteMLflow project governance

This document describes how decisions are made in LiteMLflow, who can make them, and how the project handles conflict.

## Roles

### Maintainers

Maintainers have commit access to `main` and can approve PRs. Initial maintainer set:

- Project lead (oscar@dolotov.com — placeholder until org is registered)

New maintainers are nominated by an existing maintainer and confirmed by lazy consensus among the existing set (no objection within 7 days = approved).

### Contributors

Anyone who has had a PR merged. Contributors propose changes via PR, vote on RFCs (informally), and can self-nominate to become reviewers after a sustained track record.

### Reviewers

Reviewers can give "approving" review on a PR but cannot merge unless a maintainer also approves. Becoming a reviewer requires:
- Five merged PRs over at least three months.
- Demonstrated understanding of the relevant subsystem in code reviews.
- Nomination by a maintainer.

## Decision-making

### Day-to-day technical decisions

Made on PRs and issues. Reviewers and maintainers chime in. If there's no controversy after 48 hours, the change is fine.

### Architectural decisions

Recorded in [docs/adr/](adr/). Substantive new decisions go through an RFC:
1. Open a draft RFC as a PR to `docs/adr/NNNN-<title>.md` (next sequential number).
2. Linked from a GitHub Discussion for community input.
3. Maintainers + at least one reviewer approve.
4. Status flips to "Accepted"; the implementation can begin.

### Release decisions

Releases are time-boxed (one minor per quarter). The release manager is rotated among maintainers. They cut RC1 three weeks before the target date, RC2 one week before, and final on the target date if no P0 issues are open.

### Disputes

If maintainers disagree:
1. Try to identify the underlying technical disagreement and write it down.
2. Spend up to one week on async dialog.
3. If unresolved: project lead breaks the tie. Project lead can be overruled by a 2/3 majority of maintainers (formal vote, time-boxed to one week).

This last clause is the "BDFL with override" pattern from Python and Linux. Most projects never invoke it; it exists so we don't deadlock.

## Code of Conduct enforcement

The Code of Conduct is enforced by maintainers. A user can be temporarily or permanently banned from the project after a clear violation. Bans require approval by at least two maintainers.

CoC reports go to `conduct@litemlflow.invalid` (placeholder); they are confidential. The project lead is not part of the CoC chain of trust to prevent conflict of interest.

## Repository governance

- All commits must be DCO-signed (`git commit -s`).
- No force-pushes to `main`. Maintainers force-push only with explicit consent of all reviewers on the affected branch.
- Sensitive files (`SECURITY.md`, `LICENSE`, `NOTICE`, this `governance.md`) require maintainer approval and a 7-day review window.

## Trademark and brand

LiteMLflow is the project name. The team does not claim ownership of "MLflow" — that mark belongs to Databricks, Inc. We disclaim affiliation in `NOTICE`.

If we ever need to rename (e.g., on legal advice), the rename is tracked as an ADR; existing users get a migration path of at least one minor release.

## Funding and sponsorship

Year-1 plan: no monetization. Donations via GitHub Sponsors are accepted starting Q2. Corporate sponsorship requires maintainer consensus and is documented publicly.

## Modifying this document

This document is itself governance: changes go through a PR, with at least two maintainer approvals and a 14-day comment window.
