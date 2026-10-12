package kafka

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/IBM/sarama"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
)

// claimState belongs to one assignment, not merely a partition number. In a
// cooperative rebalance a claim can end while the group session remains alive.
type claimState struct {
	ctx     context.Context
	cancel  context.CancelFunc
	session sarama.ConsumerGroupSession
	mu      sync.RWMutex
	active  bool
}

func newClaimState(session sarama.ConsumerGroupSession) *claimState {
	ctx, cancel := context.WithCancel(session.Context())
	return &claimState{ctx: ctx, cancel: cancel, session: session, active: true}
}

func (s *claimState) live() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active && s.ctx.Err() == nil
}

func (s *claimState) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
	s.cancel()
}

func (s *claimState) mark(msg *sarama.ConsumerMessage) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.active || s.ctx.Err() != nil {
		return false
	}
	// MarkMessage advances the next-to-consume offset, matching the old API.
	s.session.MarkMessage(msg, "")
	return true
}

type groupMessage struct {
	*sarama.ConsumerMessage
	claim *claimState
}

// groupConsumer bridges the existing channel API to Sarama's native handler API.
// It never commits a message merely because the broker delivered it.
type groupConsumer struct {
	group     sarama.ConsumerGroup
	ctx       context.Context
	cancel    context.CancelFunc
	topics    []string
	messages  chan *groupMessage
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	mu        sync.RWMutex
	claims    map[topicPartition]*claimState
}

func newGroupConsumer(ctx context.Context, brokers []string, groupID string, topics []string, cfg *sarama.Config) (*groupConsumer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	group, err := sarama.NewConsumerGroup(brokers, groupID, cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	g := &groupConsumer{
		group: group, ctx: ctx, cancel: cancel, topics: append([]string(nil), topics...),
		messages: make(chan *groupMessage, cfg.ChannelBufferSize), done: make(chan struct{}),
		claims: make(map[topicPartition]*claimState),
	}
	go g.run()
	return g, nil
}

func (g *groupConsumer) run() {
	defer close(g.done)
	defer close(g.messages)
	for g.ctx.Err() == nil {
		err := g.group.Consume(g.ctx, g.topics, g)
		if g.ctx.Err() != nil || errors.Is(err, sarama.ErrClosedConsumerGroup) {
			return
		}
		if err != nil {
			glog.Shared.Error("Kafka consumer group session failed", zap.Error(err))
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-g.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (g *groupConsumer) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (g *groupConsumer) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (g *groupConsumer) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	state := newClaimState(session)
	key := topicPartition{topic: claim.Topic(), partition: claim.Partition()}
	g.mu.Lock()
	g.claims[key] = state
	g.mu.Unlock()
	defer func() {
		state.stop()
		g.mu.Lock()
		if g.claims[key] == state {
			delete(g.claims, key)
		}
		g.mu.Unlock()
	}()
	for {
		select {
		case <-g.ctx.Done():
			return nil
		case <-state.ctx.Done():
			return nil
		case msg, ok := <-claim.Messages():
			if !ok {
				return nil
			}
			select {
			case g.messages <- &groupMessage{ConsumerMessage: msg, claim: state}:
			case <-g.ctx.Done():
				return nil
			case <-state.ctx.Done():
				return nil
			}
		}
	}
}

func (g *groupConsumer) Messages() <-chan *groupMessage { return g.messages }
func (g *groupConsumer) Errors() <-chan error           { return g.group.Errors() }

func (g *groupConsumer) Subscriptions() map[string][]int32 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[string][]int32)
	for key := range g.claims {
		result[key.topic] = append(result[key.topic], key.partition)
	}
	return result
}

func (g *groupConsumer) markMessage(msg *KafkaMsg) bool {
	state := msg.claim
	if state == nil {
		// Preserve explicitly constructed messages for callers of the old API.
		g.mu.RLock()
		state = g.claims[topicPartition{topic: msg.Topic, partition: msg.Partition}]
		g.mu.RUnlock()
	}
	if state == nil {
		return false
	}
	return state.mark(&sarama.ConsumerMessage{Topic: msg.Topic, Partition: msg.Partition, Offset: msg.Offset})
}

func (g *groupConsumer) Close() error {
	g.closeOnce.Do(func() {
		g.cancel()
		g.closeErr = g.group.Close()
		<-g.done
	})
	return g.closeErr
}
