package kafka

import (
	"github.com/IBM/sarama"
	"testing"
)

type namedTestStrategy struct {
	sarama.BalanceStrategy
	name string
}

func (s namedTestStrategy) Name() string { return s.name }
func TestTransportRejectsUnsupportedConsumerModes(t *testing.T) {
	for _, name := range []string{"cooperative-sticky", "unknown"} {
		cfg := validWrapperConfig()
		cfg.ClusterConfig = sarama.NewConfig()
		cfg.ClusterConfig.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{namedTestStrategy{name: name}}
		if _, err := validatedKafkaConfig(cfg); err == nil {
			t.Fatalf("unsupported lifecycle mode %s accepted", name)
		}
		if cfg.ClusterConfig.Consumer.Group.Rebalance.GroupStrategies[0].Name() != name {
			t.Fatal("caller configuration mutated")
		}
	}
}
func TestTransportCopiesNativeMemberData(t *testing.T) {
	cfg := validWrapperConfig()
	cfg.ClusterConfig = sarama.NewConfig()
	cfg.ClusterConfig.Consumer.Group.Member.UserData = []byte("original")
	effective, err := validatedKafkaConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	effective.Consumer.Group.Member.UserData[0] = 'X'
	if string(cfg.ClusterConfig.Consumer.Group.Member.UserData) != "original" {
		t.Fatal("member metadata aliases caller")
	}
}
