package kafka

import (
	"context"
	cluster "github.com/bsm/sarama-cluster"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestTransportPinnedDefaultsArePlaintext(t *testing.T) {
	cfg := cluster.NewConfig()
	if cfg.Net.TLS.Enable || cfg.Net.SASL.Enable {
		t.Fatal("pinned defaults differ from scanner evidence")
	}
	t.Log("exact pinned cluster defaults have TLS=false SASL=false")
}
func TestTransportDefaultPublicConstructorSendsTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		cli, err := NewKafkaCliWithGroupID(ctx, &KafkaCliCfg{Brokers: []string{listener.Addr().String()}, Topics: []string{"synthetic"}, Groupid: "security-wire", KMsgPool: &sync.Pool{New: func() interface{} { return &KafkaMsg{} }}})
		if cli != nil {
			cli.Close()
		}
		done <- err
	}()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	var conn net.Conn
	select {
	case conn = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("constructor did not connect to owned loopback listener")
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	first := make([]byte, 5)
	_, err = io.ReadFull(conn, first)
	conn.Close()
	listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("constructor did not fail closed after peer termination")
	}
	t.Logf("first protocol record bytes=%x", first)
	if first[0] != 0x16 || first[1] != 0x03 {
		t.Fatal("default public constructor emitted unauthenticated plaintext Kafka")
	}
}
