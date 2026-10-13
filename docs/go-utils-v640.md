# go-utils v6.4.0 consumer update

The group consumer imports go-utils/v6/log. The module update stays on v6 and
retains the current Sarama group-consumer, TLS and message ownership code.
No passwords, ciphertexts, policies or offsets are rewritten.

Baseline: 0ac49aa6699a36e3f3ab93039c59dcdc5aa89c27, go-utils v6.3.1.
Target: v6.4.0, source 6a02887d3dbf60ecadf21a67b5d3b9449fd942e6.
The existing unit/race contracts cover topic/partition commit isolation,
rebalance claims, cancellation, backpressure and concurrent Close. The secure
disposable broker harness remains the TLS/mTLS/SASL acceptance command.
Local baseline/candidate and exact-head CI results are tracked in the PR
description; a published Draft is not a qualification claim.

Adoption is a dependency-only build change. Keep the previous application
binary/image available for rollback; no persisted format or broker deployment
change is required. This task publishes source only, without merge or rollout.
