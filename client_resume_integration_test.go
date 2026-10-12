//go:build integration

package kafka

import (
	"context"
	"testing"
	"time"

	"github.com/IBM/sarama"
)

func integrationWaitOffset(t *testing.T, address, group, topic string, expected int64) {
	t.Helper()
	client, err := sarama.NewClient([]string{address}, integrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(10 * time.Second)
	actual := int64(-1)
	for time.Now().Before(deadline) {
		broker, err := client.Coordinator(group)
		if err != nil {
			t.Fatal(err)
		}
		request := &sarama.OffsetFetchRequest{Version: 1, ConsumerGroup: group}
		request.AddPartition(topic, 0)
		response, err := broker.FetchOffset(request)
		if err == nil {
			if block := response.GetBlock(topic, 0); block != nil && block.Err == sarama.ErrNoError {
				actual = block.Offset
			}
		}
		if actual == expected {
			t.Logf("broker committed offset %d for resume contract", actual)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("broker offset=%d; want %d", actual, expected)
}

// TestIntegrationResumeDoesNotCommitUnacknowledgedDelivery distinguishes a real
// broker commit from channel delivery, then rejoins the same group after Close.
func TestIntegrationResumeDoesNotCommitUnacknowledgedDelivery(t *testing.T) {
	address := integrationBroker(t)
	topic := integrationTopic(t, address)
	group := integrationName(t)
	first, _ := integrationConsumer(t, address, topic, group)
	messages := first.Messages(context.Background())
	integrationProduce(t, address, topic, 2)
	for offset := int64(0); offset < 2; offset++ {
		select {
		case msg := <-messages:
			if msg == nil || msg.Offset != offset {
				t.Fatalf("unexpected initial delivery at offset %d: %#v", offset, msg)
			}
			if offset == 0 {
				first.CommitWithMsg(msg)
			} else {
				// Simulate a processed-but-unacknowledged message without leaking
				// its pooled object. No broker acknowledgement is made here.
				first.KMsgPool.Put(msg)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("initial delivery timed out")
		}
	}
	integrationWaitOffset(t, address, group, topic, 1)
	if !integrationClose(t, first) {
		return
	}
	integrationDrain(t, first, messages)
	integrationWaitOffset(t, address, group, topic, 1)

	second, _ := integrationConsumer(t, address, topic, group)
	resumed := second.Messages(context.Background())
	select {
	case msg := <-resumed:
		if msg == nil || msg.Offset != 1 || string(msg.Message) != "synthetic-1" {
			t.Fatalf("rejoin lost or skipped unacknowledged delivery: %#v", msg)
		}
		second.CommitWithMsg(msg)
	case <-time.After(10 * time.Second):
		t.Fatal("unacknowledged offset was not redelivered after rejoin")
	}
	integrationWaitOffset(t, address, group, topic, 2)
	integrationClose(t, second)
	integrationDrain(t, second, resumed)
}
