package kafka

import (
	"context"
	"sync"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

// topicPartition identifies an independent Kafka offset sequence.
type topicPartition struct {
	topic     string
	partition int32
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
	for _, optf := range opts {
		if err = optf(f.commitCheckOption); err != nil {
			return nil, errors.Wrap(err, "set commit check option")
		}
	}
	utils.Logger.Info("NewCommitFilter",
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
	utils.Logger.Debug("start runFilterBeforeChan")
	defer utils.Logger.Debug("runFilterBeforeChan quit")
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

		// Offsets are comparable only within the same topic and partition.
		key := topicPartition{topic: kmsg.Topic, partition: kmsg.Partition}
		if record, ok = kmsgSlots[key]; !ok {
			kmsgSlots[key] = &msgRecord{
				lastCommitT: utils.Clock.GetUTCNow(),
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
	now := utils.Clock.GetUTCNow()
	utils.Logger.Debug("run filterSlots2AfterChan")
	defer utils.Logger.Debug("done filterSlots2AfterChan", zap.Duration("cost", utils.Clock.GetUTCNow().Sub(now)))

	for _, record := range kmsgSlots {
		if !record.isCommitted &&
			(record.num > f.commitCheckNum || now.Sub(record.lastCommitT) > f.commitCheckInterval) {
			if utils.Settings.GetBool("dry") {
				utils.Logger.Debug("put msg into afterChan",
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
