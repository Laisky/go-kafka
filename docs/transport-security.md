# Native consumer and verified transport migration

## Caller changes

The October 12, 2026 dependency upgrade replaces Shopify Sarama and
sarama-cluster with `github.com/IBM/sarama v1.61.1` and uses go-utils v6.3.1.
The module requires Go 1.26.9 or newer; Go 1.26.9 and 1.27.2 are qualified.
The public consume/acknowledge/close method names are retained, but configuration
and integration changes mean this is not a universal source-compatible upgrade.

`KafkaCliCfg.ClusterConfig` is now `*sarama.Config` from `github.com/IBM/sarama`.
Create it with `sarama.NewConfig()`. Replace draft sarama-cluster settings as follows:

| Legacy setting | Native Sarama setting |
| --- | --- |
| `Group.Member.UserData` | `Consumer.Group.Member.UserData` |
| `Group.PartitionStrategy` | `Consumer.Group.Rebalance.GroupStrategies` |
| `Consumer.Offsets.CommitInterval` | `Consumer.Offsets.AutoCommit.Interval` |
| Partition-stream mode | Not exposed by this wrapper |

The wrapper requires eager range, round-robin or sticky strategies. Cooperative
and unknown strategies fail validation before dialing: revoking one cooperative
claim does not necessarily cancel its whole session, which is incompatible with
the wrapper's cancellation-driven, backpressured channel lifecycle.

`ListenNotifications` retains its legacy name and drains/logs native group errors.
Rebalance events are handled internally by the native group handler lifecycle.
Use keyed `KafkaMsg` literals, not positional literals; messages include private
assignment metadata needed to reject late acknowledgements from old assignments.

The old global `go-utils.Settings` and root `go-utils.Logger` APIs are not used.
Configure the global `github.com/spf13/viper` instance before starting consumers:

```go
viper.SetConfigFile("settings.yml")
if err := viper.ReadInConfig(); err != nil {
    return err
}
// BindPFlags, GetStringSlice, GetDuration and Unmarshal remain available.
```

The `dry` flag is read through `viper.GetBool("dry")`. Do not concurrently mutate
global settings while workers are reading them. Logging uses
`github.com/Laisky/go-utils/v6/log.Shared`. Applications using legacy settings
loaders or logger configuration must adapt their own integration too.

## TLS and authentication

`NewKafkaCliWithGroupID` enables verified TLS 1.2 or newer by default. A nil TLS
configuration uses system roots and verifies broker hostnames. Existing bare
plaintext broker callers must migrate before adopting this version.

Supply `ClusterConfig.Net.TLS.Config.RootCAs` for a private CA and `Certificates`
for mutual TLS. Configure `ClusterConfig.Net.SASL` for authentication over TLS.
Load credentials from the application's normal secret source, never from a
committed example. SASL is optional so TLS-only and mutual-TLS brokers remain
supported; broker authorization is still the operator's responsibility.

The constructor rejects `InsecureSkipVerify`, obsolete TLS minimums and invalid
version ranges. Setting `Net.TLS.Enable=false` in a supplied config cannot
silently disable verification. Broker/topic slices, member metadata, balance
strategy slices, TLS config and CA pools are copied. Callback implementations,
private key objects and other shared credential data must remain safe for their
whole use lifetime; callers must not concurrently mutate them.

`AllowPlaintextForTrustedTunnel` is the sole deliberate plaintext opt-in, including
SASL credentials. It is valid only when an independently authenticated,
integrity-protected tunnel is mandatory for every connection. The library cannot
enforce that external tunnel. Do not use the flag merely to silence TLS errors.

## Offset and lifecycle semantics

Process messages in order within each topic/partition before acknowledging a
higher offset, or implement an application-side contiguous-completion barrier.
Delivery does not itself acknowledge a record. Commit filtering separates topic,
partition and assignment; old assignment acknowledgements are discarded rather
than transferred to a newly assigned partition or suppressing an offset rewind.

`CommitWithMsg` transfers pooled-message ownership asynchronously. Do not read,
mutate or return a message to its pool afterward. Calls after cancellation return
without blocking, but cannot guarantee a broker commit. `Close` is idempotent and
safe for concurrent callers; it is not a queue-flush acknowledgement API.
Uncommitted messages may be redelivered, including after restart or rebalance.

## Repeatable validation

Ordinary unit/race tests stay local. For a generated Kafka 3.9.1 TLS/mTLS/SASL
fixture, use Linux, Docker, openssl and Go 1.27.2:

```sh
go mod download
docker pull apache/kafka@sha256:4ceccc577f03f51f6af8dbfda55194d0d892f4fa7913ffbded567ce3895622ed
docker pull golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c
KAFKA_TEST_GO="$(command -v go)" python3 scripts/test_integration.py
KAFKA_TEST_GO="$(command -v go)" python3 scripts/test_secure_integration.py
```

The first command's broker is loopback-only and explicitly plaintext; it excludes
only the secure-constructor negative controls. The secure broker and test process
share an offline network namespace and publish no host ports. Certificates,
credentials, topic/group names and container identities are generated test data.
The harnesses use deadlines, resource bounds and a nonblocking shared lock
(`KAFKA_TEST_LOCK`), clean owned resources and never prune Docker globally.

Both fixtures observe committed offsets and verify unacknowledged-record
redelivery after closing/rejoining the same group. They retain cancellation,
backpressure, output ownership and concurrent Close contracts. Secure controls
reject unknown CA, wrong hostname, missing client certificate and wrong SASL.
The SASL_SSL broker explicitly sets
`listener.name.sasl_ssl.ssl.client.auth=required` for mutual TLS enforcement.

## Review and rollout boundary

The earlier Snyk-provider waiting condition was superseded by the owner's explicit
October 12 instruction to ignore Snyk as a merge gate. Existing Snyk reports/statuses
are retained; no success override, advisory-ignore policy or protection change
is used. Functional/race/broker checks and Go vulnerability reachability checks
remain independent evidence. Module-only findings are not described as reachable
application vulnerabilities, nor is a passing scan a security guarantee.

Migration acceptance is recorded in
[run 38311828382](https://github.com/Laisky/go-kafka/actions/runs/38311828382).
Final PR review records the final cleaned commit and repeat CI result.
This repository change does not rotate credentials, deploy brokers or release an
application. Roll out caller/toolchain/TLS changes together and observe group
errors, lag and duplicate handling. An application rollback may restore its prior
library pin, but must preserve a verified secure network boundary; reverting this
change otherwise restores the old plaintext default and commit/lifecycle defects.
