package kafka

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestCommitFilterOnlyEmitsEligibleUncommittedOffsets protects partition commit eligibility.
func TestCommitFilterOnlyEmitsEligibleUncommittedOffsets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, err := NewCommitFilter(ctx, &sync.Pool{New: func() interface{} { return &KafkaMsg{} }}, WithCommitFilterCheckInterval(time.Hour), WithCommitFilterCheckNum(2))
	if err != nil {
		t.Fatal(err)
	}
	slots := map[int32]*msgRecord{
		0: {num: 3, lastCommitT: time.Now(), lastOffset: 42, lastMsg: &KafkaMsg{Partition: 0, Offset: 42}},
		1: {num: 3, lastCommitT: time.Now(), lastOffset: 99, lastMsg: &KafkaMsg{Partition: 1, Offset: 99}, isCommitted: true},
		2: {num: 1, lastCommitT: time.Now(), lastOffset: 10, lastMsg: &KafkaMsg{Partition: 2, Offset: 10}},
	}
	f.filterSlots2AfterChan(slots)
	select {
	case msg := <-f.GetAfterChan():
		if msg.Partition != 0 || msg.Offset != 42 {
			t.Fatalf("ineligible offset emitted: %#v", msg)
		}
	default:
		t.Fatal("eligible offset was not emitted")
	}
	f.filterSlots2AfterChan(slots)
	select {
	case msg := <-f.GetAfterChan():
		t.Fatalf("duplicate or ineligible offset emitted: %#v", msg)
	default:
	}
}
