# go-kafka

A channel-oriented Kafka consumer, originally separated from `go-utils/kafka`.
The implementation now uses [IBM Sarama](https://github.com/IBM/sarama)'s native
consumer groups instead of the legacy Shopify Sarama / sarama-cluster pair.

## Requirements and upgrade notes

Go **1.26.9 or newer** is required. The dependency migration was qualified with
Go 1.26.9 and Go 1.27.2 and an owned Apache Kafka 3.9.1 broker.
This is not a drop-in upgrade for every legacy caller: verified TLS is now the
default, `KafkaCliCfg.ClusterConfig` uses `*sarama.Config` from
`github.com/IBM/sarama`, and legacy go-utils settings/logging integrations must
be migrated. See [the migration guide](docs/transport-security.md).

Stable release selections verified on October 12, 2026 include:

| Module | Selected version |
| --- | --- |
| `github.com/IBM/sarama` | `v1.61.1` |
| `github.com/Laisky/go-utils/v6` | `v6.3.1` |
| `github.com/golang-jwt/jwt/v5` | `v5.3.1` |
| `github.com/spf13/viper` | `v1.21.0` |
| `github.com/hashicorp/consul/api` | `v1.34.5` |
| `github.com/klauspost/compress` | `v1.20.3` |
| `github.com/pkg/sftp` | `v1.13.11` |
| `github.com/prometheus/client_golang` | `v1.25.0` |
| `google.golang.org/protobuf` | `v1.36.12` |

`go.mod` is authoritative for the full set. Direct and test dependencies were
resolved together, followed by `go get -u -t ./...` and `go mod tidy`.
Not every graph node has a stable tag: notably go-utils v6.3.1 requires
`github.com/Laisky/zap v1.27.1-0.20261006114731-55f41c2b5061`, newer than its
v1.27.0 release tag. That upstream-required revision is retained, not disguised
as a stable release or downgraded. Unimported upstream graph entries may retain
older versions; a whole-module list is not a list of production imports.

## Consumer API

`NewKafkaCliWithGroupID`, `Messages`, `CommitWithMsg`, and `Close` retain their
channel-oriented roles. Supply a `sync.Pool` whose `New` returns `*KafkaMsg`.
Use keyed struct literals; messages carry private assignment metadata.

```go
pool := &sync.Pool{New: func() interface{} { return &kafka.KafkaMsg{} }}
config := sarama.NewConfig() // import github.com/IBM/sarama
client, err := kafka.NewKafkaCliWithGroupID(ctx, &kafka.KafkaCliCfg{
    Brokers:       []string{"broker.example:9093"},
    Topics:        []string{"events"},
    Groupid:       "worker-group",
    KMsgPool:      pool,
    ClusterConfig: config,
})
if err != nil {
    return err
}
defer client.Close()
for msg := range client.Messages(ctx) {
    // Process and durably persist the message before acknowledging it.
    // Do not acknowledge a later offset before earlier work in that partition.
    if err := process(msg.Message); err != nil {
        pool.Put(msg) // return the object without acknowledging the record
        return err
    }
    client.CommitWithMsg(msg) // ownership transfers; do not reuse msg
}
```

Each `Messages` output belongs to the client and closes on cancellation or
shutdown; callers must not close it. `Close` supports repeated and concurrent
callers. Cancellation unblocks backpressured output and late acknowledgements.

Acknowledgements are asynchronous, not an exactly-once or flush API. `Close`
does **not** guarantee all queued acknowledgements have reached the broker.
The application must process messages in order within each topic/partition,
or implement its own contiguous-completion barrier before acknowledging them.
Stale assignments cannot advance a new assignment's offsets. Duplicate delivery
can occur during restart or rebalance, so downstream effects should be idempotent.

The wrapper supports eager range, round-robin and sticky assignment strategies.
Cooperative and unknown strategies are rejected because their claim-revocation
lifecycle is not compatible with this backpressured channel interface.

## Tests

Ordinary tests remain local and never start a broker:

```sh
go test -race -count=1 ./...
go vet ./...
go build ./...
go mod verify
```

Broker contracts require an explicit disposable fixture:

```sh
docker pull apache/kafka@sha256:4ceccc577f03f51f6af8dbfda55194d0d892f4fa7913ffbded567ce3895622ed
KAFKA_TEST_GO="$(command -v go)" python3 scripts/test_integration.py

docker pull golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c
KAFKA_TEST_GO="$(command -v go)" python3 scripts/test_secure_integration.py
```

Use Go 1.27.2 when running both harnesses. The plaintext fixture binds only to
loopback. The secure fixture generates its own TLS/mTLS/SASL material and shares
an offline broker/test network namespace without publishing host ports. Both
use bounded deadlines, generated topics/groups and owned-resource cleanup.
`KAFKA_TEST_LOCK` selects the nonblocking local coordination lock.

Acceptance covers broker-observed offsets, redelivery of unacknowledged records
after rejoining the same group, cancellation/backpressure, concurrent shutdown,
TLS chain/hostname verification and invalid client-certificate/SASL controls.
Unit regressions also protect topic/partition separation and assignment changes.
CI's broker, minimum-compiler and dependency-review jobs are opt-in with the
`integration` label; normal PR checks remain read-only and local.

The Go vulnerability check distinguishes imported/called findings from
module-only advisories. The October 12 review found no imported-package or
reachable-symbol findings; the unimported OpenPGP advisory GO-2026-5932 remains
module-only. This is not a guarantee of zero vulnerabilities in the full graph.
No release or production deployment is performed by the validation workflows.
