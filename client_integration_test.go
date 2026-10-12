//go:build integration
// +build integration

package kafka

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
)

// integrationBroker permits only an explicitly selected loopback broker.
func integrationBroker(t *testing.T) string {
	t.Helper()
	address := os.Getenv("KAFKA_TEST_BROKER")
	if address == "" {
		t.Skip("set KAFKA_TEST_BROKER for an owned disposable loopback broker")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatalf("integration broker must be a literal loopback address, got %q", address)
	}
	return address
}

// integrationName gives each test its own topic and consumer group.
func integrationName(t *testing.T) string {
	t.Helper()
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	return "go-kafka-test-" + hex.EncodeToString(random)
}

// integrationConfig bounds all auxiliary broker requests without changing client defaults.
func integrationConfig() *sarama.Config {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_8_0_0
	cfg.Net.DialTimeout = 3 * time.Second
	cfg.Net.ReadTimeout = 3 * time.Second
	cfg.Net.WriteTimeout = 3 * time.Second
	cfg.Metadata.Retry.Max = 2
	cfg.Metadata.Retry.Backoff = 100 * time.Millisecond
	cfg.Producer.Return.Successes = true
	cfg.Producer.RequiredAcks = sarama.WaitForAll
	if caPath := os.Getenv("KAFKA_TEST_TLS_CA"); caPath != "" {
		ca, err := os.ReadFile(caPath)
		if err != nil {
			panic(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			panic("invalid synthetic CA")
		}
		cfg.Net.TLS.Enable = true
		cfg.Net.TLS.Config = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		if certPath := os.Getenv("KAFKA_TEST_TLS_CERT"); certPath != "" {
			cert, err := tls.LoadX509KeyPair(certPath, os.Getenv("KAFKA_TEST_TLS_KEY"))
			if err != nil {
				panic(err)
			}
			cfg.Net.TLS.Config.Certificates = []tls.Certificate{cert}
		}
		if user := os.Getenv("KAFKA_TEST_SASL_USER"); user != "" {
			cfg.Net.SASL.Enable = true
			cfg.Net.SASL.Handshake = true
			cfg.Net.SASL.User = user
			cfg.Net.SASL.Password = os.Getenv("KAFKA_TEST_SASL_PASSWORD")
		}
	}
	return cfg
}

// integrationTopic creates only a generated test topic on the opted-in broker.
func integrationTopic(t *testing.T, address string) string {
	t.Helper()
	admin, err := sarama.NewClusterAdmin([]string{address}, integrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	topic := integrationName(t)
	if err := admin.CreateTopic(topic, &sarama.TopicDetail{NumPartitions: 1, ReplicationFactor: 1}, false); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.DeleteTopic(topic); _ = admin.Close() })
	return topic
}

// integrationConsumer starts the real wrapper and owns cleanup even after a failed contract.
func integrationConsumer(t *testing.T, address, topic, group string) (*KafkaCli, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pool := &sync.Pool{New: func() interface{} { return &KafkaMsg{} }}
	clusterCfg := integrationConfig()
	cli, err := NewKafkaCliWithGroupID(ctx, &KafkaCliCfg{Brokers: []string{address}, Topics: []string{topic}, Groupid: group, KMsgPool: pool, ClusterConfig: clusterCfg, AllowPlaintextForTrustedTunnel: os.Getenv("KAFKA_TEST_TLS_CA") == ""}, WithCommitFilterCheckInterval(50*time.Millisecond), WithCommitFilterCheckNum(1))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		// The underlying client is closed directly only as a test-cleanup rescue;
		// contract assertions always exercise the public wrapper first.
		done := make(chan struct{})
		go func() { _ = cli.cli.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("underlying consumer cleanup exceeded deadline")
		}
	})
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for len(cli.cli.Subscriptions()[topic]) == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("consumer group never received its test partition")
		case err := <-cli.cli.Errors():
			if err != nil {
				t.Logf("broker startup: %v", err)
			}
		case <-ticker.C:
		}
	}
	return cli, cancel
}

// integrationProduce writes synthetic payloads only to the test-owned topic.
func integrationProduce(t *testing.T, address, topic string, count int) {
	t.Helper()
	producer, err := sarama.NewSyncProducer([]string{address}, integrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	batch := make([]*sarama.ProducerMessage, count)
	for i := range batch {
		batch[i] = &sarama.ProducerMessage{Topic: topic, Value: sarama.StringEncoder(fmt.Sprintf("synthetic-%d", i))}
	}
	if err := producer.SendMessages(batch); err != nil {
		t.Fatal(err)
	}
}

// integrationClose checks that public Close returns within a local-broker deadline.
func integrationClose(t *testing.T, cli *KafkaCli) bool {
	t.Helper()
	done := make(chan struct{})
	go func() { cli.Close(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(4 * time.Second):
		// Unblock a legacy stop-channel sender after recording the failed assertion.
		go func() {
			select {
			case <-cli.stopChan:
			case <-time.After(time.Second):
			}
		}()
		t.Error("Close did not return within four seconds")
		return false
	}
}

// integrationDrain waits for the producer-owned output to close, returning every pooled message.
func integrationDrain(t *testing.T, cli *KafkaCli, messages <-chan *KafkaMsg) {
	t.Helper()
	timeout := time.After(4 * time.Second)
	for {
		select {
		case msg, ok := <-messages:
			if !ok {
				return
			}
			cli.KMsgPool.Put(msg)
		case <-timeout:
			t.Error("Messages output was not closed after cancellation or Close")
			return
		}
	}
}

// TestIntegrationConsumeAndCommit observes the committed offset on the actual broker.
func TestIntegrationConsumeAndCommit(t *testing.T) {
	address := integrationBroker(t)
	topic := integrationTopic(t, address)
	group := integrationName(t)
	cli, _ := integrationConsumer(t, address, topic, group)
	messages := cli.Messages(context.Background())
	integrationProduce(t, address, topic, 1)
	var msg *KafkaMsg
	select {
	case msg = <-messages:
	case <-time.After(10 * time.Second):
		t.Fatal("no message consumed")
	}
	if msg == nil || string(msg.Message) != "synthetic-0" || msg.Topic != topic {
		t.Fatalf("unexpected message: %#v", msg)
	}
	expected, partition := msg.Offset+1, msg.Partition
	cli.CommitWithMsg(msg)
	client, err := sarama.NewClient([]string{address}, integrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(10 * time.Second)
	committed := int64(-1)
	for time.Now().Before(deadline) {
		coordinator, err := client.Coordinator(group)
		if err != nil {
			t.Fatal(err)
		}
		request := &sarama.OffsetFetchRequest{Version: 1, ConsumerGroup: group}
		request.AddPartition(topic, partition)
		response, err := coordinator.FetchOffset(request)
		if err == nil {
			if block := response.GetBlock(topic, partition); block != nil && block.Err == sarama.ErrNoError {
				committed = block.Offset
			}
		}
		if committed == expected {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("broker observed committed offset %d, expected %d", committed, expected)
	if committed != expected {
		t.Errorf("broker committed offset %d, want %d", committed, expected)
	}
	integrationClose(t, cli)
	integrationDrain(t, cli, messages)
}

// TestIntegrationCloseAfterCancellation checks Close after the parent's workers have stopped.
func TestIntegrationCloseAfterCancellation(t *testing.T) {
	address := integrationBroker(t)
	topic := integrationTopic(t, address)
	cli, cancel := integrationConsumer(t, address, topic, integrationName(t))
	messageCtx, messageCancel := context.WithCancel(context.Background())
	defer messageCancel()
	messages := cli.Messages(messageCtx)
	cancel()
	messageCancel()
	// The parent context has no public completion signal; allow workers a bounded grace period.
	time.Sleep(100 * time.Millisecond)
	integrationClose(t, cli)
	integrationDrain(t, cli, messages)
}

// TestIntegrationCloseBroadcasts checks one Close stops all message streams without parent cancellation.
func TestIntegrationCloseBroadcasts(t *testing.T) {
	address := integrationBroker(t)
	topic := integrationTopic(t, address)
	cli, _ := integrationConsumer(t, address, topic, integrationName(t))
	streams := []<-chan *KafkaMsg{cli.Messages(context.Background()), cli.Messages(context.Background())}
	if integrationClose(t, cli) {
		for _, stream := range streams {
			integrationDrain(t, cli, stream)
		}
	}
}

// TestIntegrationMessagesCancellationUnderBackpressure checks a full delivery queue can still stop.
func TestIntegrationMessagesCancellationUnderBackpressure(t *testing.T) {
	address := integrationBroker(t)
	topic := integrationTopic(t, address)
	cli, _ := integrationConsumer(t, address, topic, integrationName(t))
	ctx, cancel := context.WithCancel(context.Background())
	messages := cli.Messages(ctx)
	integrationProduce(t, address, topic, 1002)
	deadline := time.Now().Add(10 * time.Second)
	for len(messages) < cap(messages) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(messages) != cap(messages) {
		cancel()
		t.Fatal("delivery queue did not fill")
	}
	cancel()
	// Drain is intentionally after cancellation, so delivery cannot depend on an active reader.
	integrationDrain(t, cli, messages)
	integrationClose(t, cli)
}

// TestIntegrationConcurrentClose checks repeated Close calls share one shutdown safely.
func TestIntegrationConcurrentClose(t *testing.T) {
	address := integrationBroker(t)
	topic := integrationTopic(t, address)
	cli, _ := integrationConsumer(t, address, topic, integrationName(t))
	done := make(chan struct{}, 3)
	for i := 0; i < 3; i++ {
		go func() { cli.Close(); done <- struct{}{} }()
	}
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			go func() {
				select {
				case <-cli.stopChan:
				case <-time.After(time.Second):
				}
			}()
			t.Error("concurrent Close did not complete")
			return
		}
	}
}

func TestIntegrationSecureConstructorRejectsUntrustedAndMismatchedPeers(t *testing.T) {
	address := integrationBroker(t)
	if os.Getenv("KAFKA_TEST_TLS_CA") == "" {
		t.Fatal("secure acceptance requires synthetic TLS CA")
	}
	for _, mode := range []string{"unknown CA", "wrong hostname", "missing client certificate", "wrong SASL"} {
		t.Run(mode, func(t *testing.T) {
			cfg := integrationConfig()
			cfg.Net.DialTimeout = time.Second
			cfg.Net.ReadTimeout = time.Second
			cfg.Net.WriteTimeout = time.Second
			cfg.Metadata.Retry.Max = 0
			switch mode {
			case "unknown CA":
				cfg.Net.TLS.Config.RootCAs = x509.NewCertPool()
			case "wrong hostname":
				cfg.Net.TLS.Config.ServerName = "wrong.invalid"
			case "missing client certificate":
				cfg.Net.TLS.Config.Certificates = nil
			case "wrong SASL":
				cfg.Net.SASL.Password = "deliberately-wrong-synthetic"
			}
			pool := &sync.Pool{New: func() interface{} { return &KafkaMsg{} }}
			cli, err := NewKafkaCliWithGroupID(context.Background(), &KafkaCliCfg{Brokers: []string{address}, Topics: []string{"synthetic-denied"}, Groupid: integrationName(t), KMsgPool: pool, ClusterConfig: cfg})
			if cli != nil {
				cli.Close()
			}
			if err == nil {
				t.Fatal("invalid protected transport accepted")
			}
		})
	}
}
