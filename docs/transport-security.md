# Verified broker transport and issue 4

NewKafkaCliWithGroupID now requires verified TLS by default (TLS 1.2 or newer).
Existing plaintext broker callers must migrate before adopting this change.
Nil ClusterConfig uses the system CA pool and verifies each broker hostname.
Supply ClusterConfig with Net.TLS.Config.RootCAs for private CAs, Certificates
for mutual TLS, and Net.SASL settings for broker authentication over TLS.
The constructor copies the configuration and TLS/CA pools; callers must not
concurrently mutate other shared callback or credential data during construction.
InsecureSkipVerify and TLS versions below 1.2 are rejected. TLS Enable=false
in a supplied configuration cannot silently turn protection off.

The explicit AllowPlaintextForTrustedTunnel flag is only for deployments where
an independently authenticated, integrity-protected tunnel is mandatory on
every connection. The library cannot enforce that external tunnel. Bare
plaintext listeners must move to TLS; do not set this flag to silence errors.
SASL is optional because TLS-only brokers are supported; authentication
requirements and authorization remain the broker operator's responsibility.
This change does not rotate credentials or deploy brokers.

The scanner's prerequisites were confirmed locally: the pinned constructor
defaults emitted plaintext protocol bytes 0000001400. The repaired public
constructor emits TLS ClientHello bytes 16030105c6. This proves the library
behavior; it does not establish any production network or tenant exposure.

## Repeatable validation

Run ordinary unit/race tests with Go 1.26.9 or 1.27.2. For a real synthetic
Kafka 3.9.1 TLS/mTLS/SASL broker, run:

    go mod download
    python3 scripts/test_secure_integration.py

The Linux harness requires Docker, openssl and a host Go command for cache
locations (override KAFKA_TEST_GO). Pull the pinned Kafka and Go image digests
declared in the script first. It takes the shared nonblocking validation lock
(KAFKA_TEST_LOCK can select a coordinated alternate lock). No fixture starts
when occupied. It creates only generated certificates, synthetic credentials,
topics and group IDs. Broker and test containers share an offline network
namespace, publish no host ports, and have CPU/memory/deadline bounds. Source
is read-only; only the declared Go caches are writable. It cleans its labelled
container and temporary materials, preserves a nonzero exit on failure, and
never prunes Docker globally.

The acceptance observes committed offset 1 and retains cancellation,
backpressure, output ownership and concurrent Close contracts. Negative
controls require unknown CA, wrong hostname, absent client certificate and
bad SASL credentials to fail through the public constructor. Kafka SASL_SSL
requires listener.name.sasl_ssl.ssl.client.auth=required to enforce mutual
TLS; the fixture sets this listener-specific property explicitly.

The existing scripts/test_integration.py remains an explicitly opted-in
plaintext local fixture and excludes only the secure acceptance test; it
continues testing lifecycle/offset contracts.

## Delivery boundary

The transport candidate is stacked on the exact reviewed dependency/lifecycle
candidate in PR #3, head 3643ab691765038fa1df079cec6ec2a894ce6378. It does not
change module versions, security workflows or provider settings. PR #3's
Snyk/JWT mapping rejection remains a merge gate: no retry, waiver, or merge
into that blocked branch is part of this work. Issue #4 remains open until
qualified delivery.
