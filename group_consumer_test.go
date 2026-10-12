package kafka

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
)

type recordingSession struct {
	sarama.ConsumerGroupSession
	ctx context.Context
	mu sync.Mutex
	offsets []int64
}
func (s *recordingSession) Context() context.Context { return s.ctx }
func (s *recordingSession) MarkMessage(msg *sarama.ConsumerMessage, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets = append(s.offsets, msg.Offset)
}
func (s *recordingSession) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.offsets)
}

type syntheticClaim struct {
	sarama.ConsumerGroupClaim
	messages <-chan *sarama.ConsumerMessage
}
func (c syntheticClaim) Topic() string { return "events" }
func (c syntheticClaim) Partition() int32 { return 0 }
func (c syntheticClaim) Messages() <-chan *sarama.ConsumerMessage { return c.messages }

func TestClaimAcknowledgementsRejectEndedAssignments(t *testing.T) {
	session := &recordingSession{ctx: context.Background()}
	old := newClaimState(session)
	defer old.stop()
	msg := &sarama.ConsumerMessage{Topic: "events", Partition: 0, Offset: 20}
	if !old.mark(msg) || session.count() != 1 { t.Fatal("active assignment acknowledgement lost") }
	old.stop()
	if old.mark(msg) || session.count() != 1 { t.Fatal("ended assignment marked an offset") }
	current := newClaimState(session)
	defer current.stop()
	g := &groupConsumer{claims: map[topicPartition]*claimState{{topic: "events", partition: 0}: current}}
	if g.markMessage(&KafkaMsg{Topic: "events", Partition: 0, Offset: 99, claim: old}) { t.Fatal("stale acknowledgement crossed assignment boundary") }
	if !g.markMessage(&KafkaMsg{Topic: "events", Partition: 0, Offset: 0, claim: current}) { t.Fatal("new assignment offset rewind was discarded") }
	if session.count() != 2 { t.Fatalf("unexpected marks: %d", session.count()) }
}

func TestNativeClaimBackpressureHonorsSessionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &recordingSession{ctx: ctx}
	input := make(chan *sarama.ConsumerMessage, 1)
	input <- &sarama.ConsumerMessage{Topic: "events", Partition: 0, Offset: 0}
	g := &groupConsumer{ctx: context.Background(), messages: make(chan *groupMessage), claims: make(map[topicPartition]*claimState)}
	done := make(chan error, 1)
	go func() { done <- g.ConsumeClaim(session, syntheticClaim{messages: input}) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(g.Subscriptions()["events"]) == 0 {
		select {
		case <-deadline.C: t.Fatal("claim did not start")
		default: time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done: if err != nil { t.Fatal(err) }
	case <-time.After(time.Second): t.Fatal("backpressured claim ignored cancellation")
	}
	if len(g.Subscriptions()) != 0 { t.Fatal("ended assignment stayed subscribed") }
	if session.count() != 0 { t.Fatal("delivery automatically acknowledged a record") }
}

func TestCommitAfterCancellationDoesNotBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cli := &KafkaCli{ctx: ctx, KafkaCliCfg: &KafkaCliCfg{KMsgPool: &sync.Pool{}}, beforeChan: make(chan *KafkaMsg)}
	done := make(chan struct{})
	go func() { cli.CommitWithMsg(&KafkaMsg{Topic: "events"}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second): t.Fatal("late acknowledgement blocked after cancellation")
	}
}

func TestCommitFilterPrunesRevokedClaimAndAllowsOffsetRewind(t *testing.T) {
	old := newClaimState(&recordingSession{ctx: context.Background()})
	current := newClaimState(&recordingSession{ctx: context.Background()})
	defer old.stop()
	defer current.stop()
	pool := &sync.Pool{}
	f := &CommitFilter{kMsgPool: pool, commitCheckOption: &commitCheckOption{commitCheckNum: 0, commitCheckInterval: time.Hour}, afterChan: make(chan *KafkaMsg, 2)}
	slots := map[topicPartition]*msgRecord{
		{topic: "events", partition: 0, claim: old}: {num: 1, lastOffset: 99, lastMsg: &KafkaMsg{Topic: "events", Partition: 0, Offset: 99, claim: old}},
		{topic: "events", partition: 0, claim: current}: {num: 1, lastOffset: 0, lastMsg: &KafkaMsg{Topic: "events", Partition: 0, Offset: 0, claim: current}},
	}
	old.stop()
	f.filterSlots2AfterChan(slots)
	if len(slots) != 1 { t.Fatal("revoked assignment was not pruned") }
	select {
	case msg := <-f.afterChan:
		if msg.claim != current || msg.Offset != 0 { t.Fatal("wrong assignment or offset emitted") }
	default: t.Fatal("new assignment offset was lost")
	}
	if len(f.afterChan) != 0 { t.Fatal("revoked assignment was emitted") }
}

func TestInvalidCommitOptionsReturnErrors(t *testing.T) {
	pool := &sync.Pool{}
	for _, option := range []CommitFilterOptFunc{WithCommitFilterCheckInterval(0), WithCommitFilterCheckNum(-1), WithCommitFilterCheckChanSize(-1), nil} {
		if _, err := NewCommitFilter(context.Background(), pool, option); err == nil { t.Fatal("invalid commit option accepted") }
	}
}
