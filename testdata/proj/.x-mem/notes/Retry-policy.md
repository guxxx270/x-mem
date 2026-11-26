---
id: retry-policy
title: Payment callback retry policy
type: decision
---
Payment callbacks retry 3 times with exponential backoff (1s, 4s, 16s), then go to the dead-letter queue.
