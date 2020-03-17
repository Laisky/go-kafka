package kafka

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
)

func ExampleKafkaCli() {
	var (
		kmsgPool = &sync.Pool{
			New: func() interface{} {
				return &KafkaMsg{}
			},
		}
	)
	cli, err := NewKafkaCliWithGroupID(
		context.Background(),
		&KafkaCliCfg{
			Brokers:  []string{"brokers url here"},
			Topics:   []string{"topics name here"},
			Groupid:  "group id",
			KMsgPool: kmsgPool,
		},
		WithCommitFilterCheckInterval(5*time.Second),
		WithCommitFilterCheckNum(100),
	)
	if err != nil {
		panic(errors.Wrap(err, "try to connect to kafka got error"))
	}

	for kmsg := range cli.Messages(context.Background()) {
		// do something with kafka message
		fmt.Println(string(kmsg.Message))
		cli.CommitWithMsg(kmsg) // async commit
	}
}
