package kafka

import (
	"context"
	"sync"
	"time"

	"github.com/IBM/sarama"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

const (
	defaultCommitCheckInterval = 3 * time.Second
	defaultCommitCheckNum      = 10000
	defaultCommitCheckChanSize = 10000
)

type commitCheckOption struct {
	commitCheckInterval time.Duration
	commitCheckNum,
	commitCheckChanSize int
}

// CommitFilterOptFunc configures the asynchronous commit filter.
type CommitFilterOptFunc func(*commitCheckOption) error

// WithCommitFilterCheckInterval sets the commit eligibility interval.
func WithCommitFilterCheckInterval(interval time.Duration) CommitFilterOptFunc {
	return func(opt *commitCheckOption) error {
		opt.commitCheckInterval = interval
		return nil
	}
}

// WithCommitFilterCheckNum sets the commit eligibility count.
func WithCommitFilterCheckNum(num int) CommitFilterOptFunc {
	return func(opt *commitCheckOption) error {
		opt.commitCheckNum = num
		return nil
	}
}

// WithCommitFilterCheckChanSize sets the commit queue size.
func WithCommitFilterCheckChanSize(size int) CommitFilterOptFunc {
	return func(opt *commitCheckOption) error {
		opt.commitCheckChanSize = size
		return nil
	}
}

// KafkaMsg is a pooled Kafka message. CommitWithMsg transfers its ownership.
type KafkaMsg struct {
	Topic     string
	Message   []byte
	Offset    int64
	Partition int32
	Timestamp time.Time
	claim     *claimState
}

// KafkaCliCfg configures the group consumer.
type KafkaCliCfg struct {
	Brokers, Topics []string
	Groupid         string
	KMsgPool        *sync.Pool
	// ClusterConfig supplies native IBM Sarama settings, including TLS and SASL.
	// Nil selects defaults. Configuration must not be mutated during construction.
	ClusterConfig *sarama.Config
	// AllowPlaintextForTrustedTunnel requires an independently authenticated,
	// integrity-protected tunnel on every connection. The caller enforces it.
	AllowPlaintextForTrustedTunnel bool
}

// KafkaCli is a channel-oriented Kafka group consumer.
type KafkaCli struct {
	*KafkaCliCfg
	ctx       context.Context
	stopChan  chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc

	cli                   *groupConsumer
	beforeChan, afterChan chan *KafkaMsg
}

// NewKafkaCliWithGroupID creates a consumer with verified TLS by default.
func NewKafkaCliWithGroupID(ctx context.Context, cfg *KafkaCliCfg, opts ...CommitFilterOptFunc) (k *KafkaCli, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		if err != nil {
			cancel()
		}
	}()
	config, err := validatedKafkaConfig(cfg)
	if err != nil {
		return nil, err
	}
	// Validate commit options before any broker operation.
	cf, err := NewCommitFilter(ctx, cfg.KMsgPool, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "new CommitFilter")
	}
	consumer, err := newGroupConsumer(ctx, cfg.Brokers, cfg.Groupid, cfg.Topics, config)
	if err != nil {
		return nil, errors.Wrap(err, "create Kafka consumer")
	}
	owned := *cfg
	owned.Brokers = append([]string(nil), cfg.Brokers...)
	owned.Topics = append([]string(nil), cfg.Topics...)
	owned.ClusterConfig = config
	k = &KafkaCli{
		KafkaCliCfg: &owned, ctx: ctx, cli: consumer,
		stopChan: make(chan struct{}), cancel: cancel,
		beforeChan: cf.GetBeforeChan(), afterChan: cf.GetAfterChan(),
	}
	go k.ListenNotifications(ctx)
	go k.runCommitor(ctx)
	return k, nil
}

// Close cancels workers and closes the native group exactly once. It does not
// guarantee that every asynchronously queued acknowledgement has been committed.
func (k *KafkaCli) Close() {
	k.closeOnce.Do(func() {
		close(k.stopChan)
		k.cancel()
		if err := k.cli.Close(); err != nil {
			glog.Shared.Error("close Kafka consumer", zap.Error(err))
		}
	})
}

// ListenNotifications retains the legacy method name and drains native group
// errors. Sarama owns rebalance notifications through its handler lifecycle.
func (k *KafkaCli) ListenNotifications(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-k.stopChan:
			return
		case err, ok := <-k.cli.Errors():
			if !ok {
				return
			}
			if err != nil {
				glog.Shared.Error("Kafka consumer notification", zap.Error(err))
			}
		}
	}
}

// Messages returns a producer-owned channel. Cancellation or Close closes it.
// The caller owns each pooled message until calling CommitWithMsg.
func (k *KafkaCli) Messages(ctx context.Context) <-chan *KafkaMsg {
	out := make(chan *KafkaMsg, 1000)
	go func() {
		defer close(out)
		for {
			var msg *groupMessage
			var ok bool
			select {
			case <-ctx.Done():
				return
			case <-k.ctx.Done():
				return
			case msg, ok = <-k.cli.Messages():
				if !ok {
					return
				}
			}
			if msg == nil || !msg.claim.live() {
				continue
			}
			pooled := k.KMsgPool.Get().(*KafkaMsg)
			*pooled = KafkaMsg{Topic: msg.Topic, Message: msg.Value, Offset: msg.Offset,
				Partition: msg.Partition, Timestamp: msg.Timestamp, claim: msg.claim}
			select {
			case out <- pooled:
			case <-ctx.Done():
				k.KMsgPool.Put(pooled)
				return
			case <-k.ctx.Done():
				k.KMsgPool.Put(pooled)
				return
			}
		}
	}()
	return out
}

func (k *KafkaCli) runCommitor(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-k.stopChan:
			return
		case msg, ok := <-k.afterChan:
			if !ok {
				return
			}
			if msg == nil {
				continue
			}
			if !viper.GetBool("dry") {
				k.cli.markMessage(msg)
			}
			k.KMsgPool.Put(msg)
		}
	}
}

// CommitWithMsg asynchronously transfers ownership of a message. Late
// acknowledgements after cancellation are discarded without blocking. Process
// records in order within each topic/partition before acknowledging higher offsets.
func (k *KafkaCli) CommitWithMsg(msg *KafkaMsg) {
	if msg == nil {
		return
	}
	select {
	case <-k.ctx.Done():
		k.KMsgPool.Put(msg)
		return
	default:
	}
	select {
	case k.beforeChan <- msg:
	case <-k.ctx.Done():
		k.KMsgPool.Put(msg)
	}
}
