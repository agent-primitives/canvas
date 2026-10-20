# envctl 0.9 — fictional draft announcement

This is a fictional draft only. Nothing was sent, approved, or externally published.

## Default lifetime change

For **newly created** environments that omit an explicit lifetime, envctl 0.9 changes the default from 24 hours to 8 hours. [pr-lifetime] Existing environments are unchanged, including expiry timestamps created under 0.8. [pr-lifetime] [test-existing]

## Upgrade consideration

This is not a universal seamless-upgrade or no-action-required change. New environments used by overnight or other long pipelines may require an explicit lifetime: a simulated 12-hour pipeline lost a newly created environment using the omitted-lifetime default before its final step. This was expected expiry behavior in the test. [test-overnight]

A lifetime longer than 8 hours is allowed only when organisation policy permits it. A 24-hour request is not guaranteed to be allowed; check with the organisation administrator. No universal override or automatic migration is supplied. [policy-lifetime]

## Unspecified details

The supplied material does not specify CLI syntax, exact organisation-policy configuration, recovery behavior after expiry, production SLOs, or performance benchmarks. [policy-scope]
