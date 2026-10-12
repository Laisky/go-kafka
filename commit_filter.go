package kafka

import (
	"context"
	"sync"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

// topicPartition identifies an independent Kafka offset sequence.
type topicPartition struct {
	topic     string
	partition int32
	claim     *claimState
}

type msgRecord struct {
	num         int
	lastCommitT time.Time
	lastOffset  int64
	lastMsg     *KafkaMsg
	isCommitted bool
}

// CommitFilter buffer to lazy commit kafka message
type CommitFilter struct {
	*commitCheckOption
	kMsgPool              *sync.Pool
	beforeChan, afterChan chan *KafkaMsg
}

// NewCommitFilter create new CommitFilter
func NewCommitFilter(ctx context.Context, kMsgPool *sync.Pool, opts ...CommitFilterOptFunc) (f *CommitFilter, err error) {
	f = &CommitFilter{
		kMsgPool: kMsgPool,
		commitCheckOption: &commitCheckOption{
			commitCheckInterval: defaultCommitCheckInterval,
			commitCheckNum:      defaultCommitCheckNum,
			commitCheckChanSize: defaultCommitCheckChanSize,
		},
	}
	if kMsgPool == nil {
		return nil, errors.New("message pool is required")
	}
	for _, optf := range opts {
		if optf == nil {
			return nil, errors.New("nil commit option")
		}
		if err = optf(f.commitCheckOption); err != nil {
			return nil, errors.Wrap(err, "set commit check option")
		}
	}
	if f.commitCheckInterval <= 0 || f.commitCheckNum < 0 || f.commitCheckChanSize < 0 {
		return nil, errors.New("commit interval must be positive and count/queue size nonnegative")
	}
	glog.Shared.Info("NewCommitFilter",
		zap.Int("chan_size", f.commitCheckChanSize),
		zap.Duration("interval_duration", f.commitCheckInterval),
		zap.Int("interval_num", f.commitCheckNum))

	f.beforeChan = make(chan *KafkaMsg, f.commitCheckChanSize)
	f.afterChan = make(chan *KafkaMsg, f.commitCheckChanSize)
	go f.runFilterBeforeChan(ctx)
	return f, nil
}

// GetBeforeChan get channel send message in CommitFilter
func (f *CommitFilter) GetBeforeChan() chan *KafkaMsg {
	return f.beforeChan
}

// GetAfterChan get channel out of GetAfterChan
func (f *CommitFilter) GetAfterChan() chan *KafkaMsg {
	return f.afterChan
}

// runFilterBeforeChan caches the latest message for each topic and partition,
// then invokes filterSlots2AfterChan at a fixed frequency.
func (f *CommitFilter) runFilterBeforeChan(ctx context.Context) {
	glog.Shared.Debug("start runFilterBeforeChan")
	defer glog.Shared.Debug("runFilterBeforeChan quit")
	var (
		kmsgSlots              = map[topicPartition]*msgRecord{}
		kmsg                   *KafkaMsg
		record                 *msgRecord
		ok                     bool
		scanSlots2CommitTicker = time.NewTicker(1 * time.Second)
	)
	defer scanSlots2CommitTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-scanSlots2CommitTicker.C:
			f.filterSlots2AfterChan(kmsgSlots)
			continue
		case kmsg, ok = <-f.beforeChan:
			if !ok {
				return
			}
		}

		if kmsg == nil {
			continue
		}
		if kmsg.claim != nil && !kmsg.claim.live() {
			f.kMsgPool.Put(kmsg)
			continue
		}
		// Offsets are comparable only within one topic, partition and assignment.
		key := topicPartition{topic: kmsg.Topic, partition: kmsg.Partition, claim: kmsg.claim}
		if record, ok = kmsgSlots[key]; !ok {
			kmsgSlots[key] = &msgRecord{
				lastCommitT: time.Now().UTC(),
				lastMsg:     kmsg,
				lastOffset:  kmsg.Offset,
				num:         1,
				isCommitted: false,
			}
			continue
		}

		// record already exists
		if kmsg.Offset <= record.lastOffset {
			// current kmsg's offset is smaller than exists record,
			// discard current kmsg
			f.kMsgPool.Put(kmsg)
		} else {
			// current kmsg's offset is later than exist's,
			// discard old record
			if !record.isCommitted {
				// only recycle uncommitted msg at here,
				// let commitor to recycle committed msg
				f.kMsgPool.Put(record.lastMsg)
			}

			record.lastMsg = kmsg
			record.lastOffset = kmsg.Offset
			record.isCommitted = false
		}

		record.num++
	}
}

// filterSlots2AfterChan filter all records in kmsgSlots,
// put kmsg that match `IntervalDuration` and `IntervalNum` conditions
// into innerChan to commit to kafka.
func (f *CommitFilter) filterSlots2AfterChan(kmsgSlots map[topicPartition]*msgRecord) {
	now := time.Now().UTC()
	glog.Shared.Debug("run filterSlots2AfterChan")
	defer glog.Shared.Debug("done filterSlots2AfterChan", zap.Duration("cost", time.Now().UTC().Sub(now)))

	for key, record := range kmsgSlots {
		if key.claim != nil && !key.claim.live() {
			if !record.isCommitted {
				f.kMsgPool.Put(record.lastMsg)
			}
			delete(kmsgSlots, key)
			continue
		}
		if !record.isCommitted &&
			(record.num > f.commitCheckNum || now.Sub(record.lastCommitT) > f.commitCheckInterval) {
			if viper.GetBool("dry") {
				glog.Shared.Debug("put msg into afterChan",
					zap.Time("last_commit_time", record.lastCommitT),
					zap.Int("num", record.num))
				continue
			}

			select {
			case f.afterChan <- record.lastMsg:
				record.lastCommitT = now
				record.num = 0
				record.isCommitted = true
			default:
			}
		}
	}
}
