### Fix fleet probation through normalized model routes

- Allow the read-only `/models` probe after the request logger strips `/v1` prefixes. The first live canary correctly stayed in probation because this normalized path was previously blocked.
- Exercise bare, versioned and repeatedly versioned model paths through the actual application router with probation and drain markers present; inference remains blocked. The regression test fails before the fix.
