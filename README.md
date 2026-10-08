# go-kafka

seperated from `go-utils/kafka`


## Tests

`go test ./...` stays local and does not start a broker. Broker lifecycle contracts
are opt-in through the `integration` build tag. Run them only with the owned
disposable broker harness:

```sh
docker pull apache/kafka:3.9.1
KAFKA_TEST_GO=/path/to/qualified/go python3 scripts/test_integration.py
```

The harness uses the tested Apache Kafka 3.9.1 image digest, one generated
container/network, a loopback-only random port, generated topics/group IDs,
bounded startup/test deadlines, and owned-resource cleanup. It acquires the
shared validation lock without waiting and starts nothing if the lock is occupied.
Override `KAFKA_TEST_LOCK` only when coordinating another local validation host.
Its single-node KRaft settings follow the [Apache Kafka example](https://github.com/apache/kafka/blob/3.9.1/docker/examples/docker-compose-files/single-node/plaintext/docker-compose.yml).

The tests reject non-loopback broker addresses. They exercise the actual group
consumer, observe committed offsets through the broker, and check shutdown after
cancellation, output-channel ownership, backpressure and concurrent Close calls.
They do not use production hosts or credentials.

`Close` is safe after cancellation and for repeated or concurrent callers.
Each `Messages` output is owned and closed by the client when its message context
or the client stops; callers must not close it. Delivered pooled messages pass
ownership to `CommitWithMsg`, which remains asynchronous. The integration suite
observes a broker commit before shutdown; it does not add a flush guarantee.
