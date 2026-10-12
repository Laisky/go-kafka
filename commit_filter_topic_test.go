package kafka

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestCommitFilterSeparatesTopics verifies that Kafka offsets are scoped to both
// topic and partition. Different topics may reuse partition IDs and offsets.
func TestCommitFilterSeparatesTopics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offsets [2]int64
	}{
		{name: "equal offsets", offsets: [2]int64{7, 7}},
		{name: "increasing offsets", offsets: [2]int64{7, 99}},
		{name: "decreasing offsets", offsets: [2]int64{99, 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pool := &sync.Pool{New: func() interface{} { return &KafkaMsg{} }}
			filter, err := NewCommitFilter(ctx, pool,
				WithCommitFilterCheckInterval(time.Millisecond),
				WithCommitFilterCheckNum(0),
				WithCommitFilterCheckChanSize(2))
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]int64{"topic-a": tc.offsets[0], "topic-b": tc.offsets[1]}
			filter.GetBeforeChan() <- &KafkaMsg{Topic: "topic-a", Partition: 0, Offset: tc.offsets[0]}
			filter.GetBeforeChan() <- &KafkaMsg{Topic: "topic-b", Partition: 0, Offset: tc.offsets[1]}
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			for len(want) != 0 {
				select {
				case msg := <-filter.GetAfterChan():
					if msg == nil {
						t.Fatal("commit filter returned a nil message")
					}
					offset, ok := want[msg.Topic]
					if !ok || msg.Partition != 0 || msg.Offset != offset {
						t.Fatalf("unexpected commit: topic=%q partition=%d offset=%d", msg.Topic, msg.Partition, msg.Offset)
					}
					delete(want, msg.Topic)
					pool.Put(msg)
				case <-deadline.C:
					t.Fatalf("commit filter lost independent topic offsets: %v", want)
				}
			}
		})
	}
}
