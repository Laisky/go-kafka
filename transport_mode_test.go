package kafka

import (
	"testing"

	cluster "github.com/bsm/sarama-cluster"
)

// TestTransportRejectsUnsupportedConsumerModes prevents caller configuration
// from disabling the only message stream exposed by this wrapper.
func TestTransportRejectsUnsupportedConsumerModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode cluster.ConsumerMode
	}{
		{name: "partition streams", mode: cluster.ConsumerModePartitions},
		{name: "unknown mode", mode: cluster.ConsumerMode(255)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validWrapperConfig()
			cfg.ClusterConfig = cluster.NewConfig()
			cfg.ClusterConfig.Group.Mode = tc.mode
			if _, err := validatedKafkaConfig(cfg); err == nil {
				t.Fatalf("unsupported consumer mode %d was accepted", tc.mode)
			}
			if cfg.ClusterConfig.Group.Mode != tc.mode {
				t.Fatal("validation changed caller-owned configuration")
			}
		})
	}
}
