package kafka

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/IBM/sarama"
)

func validatedKafkaConfig(cfg *KafkaCliCfg) (*sarama.Config, error) {
	if cfg == nil || len(cfg.Brokers) == 0 || len(cfg.Topics) == 0 || cfg.Groupid == "" || cfg.KMsgPool == nil {
		return nil, fmt.Errorf("brokers, topics, group ID and message pool are required")
	}
	for _, broker := range cfg.Brokers {
		if broker == "" || strings.Contains(broker, "@") {
			return nil, fmt.Errorf("invalid Kafka broker address")
		}
	}
	config := sarama.NewConfig()
	if cfg.ClusterConfig != nil {
		*config = *cfg.ClusterConfig
		config.Consumer.Group.Member.UserData = append([]byte(nil), cfg.ClusterConfig.Consumer.Group.Member.UserData...)
		config.Consumer.Group.Rebalance.GroupStrategies = append([]sarama.BalanceStrategy(nil), cfg.ClusterConfig.Consumer.Group.Rebalance.GroupStrategies...)
	}
	// Preserve the original eager-rebalance contract. Cooperative claims may be
	// revoked without cancelling the session, which a backpressured channel API
	// cannot use as its shutdown signal. Reject unsupported strategies before dial.
	strategies := config.Consumer.Group.Rebalance.GroupStrategies
	if legacy := config.Consumer.Group.Rebalance.Strategy; legacy != nil {
		strategies = append(append([]sarama.BalanceStrategy(nil), strategies...), legacy)
	}
	for _, strategy := range strategies {
		if strategy == nil {
			return nil, fmt.Errorf("nil Kafka balance strategy")
		}
		switch strategy.Name() {
		case "range", "roundrobin", "sticky":
		default:
			return nil, fmt.Errorf("Kafka wrapper requires an eager balance strategy, got %q", strategy.Name())
		}
	}
	config.Net.KeepAlive = 30 * time.Second
	config.Consumer.Return.Errors = true
	config.Consumer.Offsets.AutoCommit.Interval = time.Second
	config.Net.TLS.Enable = !cfg.AllowPlaintextForTrustedTunnel
	if config.Net.TLS.Enable {
		if config.Net.TLS.Config == nil {
			config.Net.TLS.Config = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			config.Net.TLS.Config = config.Net.TLS.Config.Clone()
			if config.Net.TLS.Config.InsecureSkipVerify {
				return nil, fmt.Errorf("Kafka TLS must verify certificate chains and broker hostnames")
			}
			if config.Net.TLS.Config.MinVersion == 0 {
				config.Net.TLS.Config.MinVersion = tls.VersionTLS12
			}
			if config.Net.TLS.Config.MinVersion < tls.VersionTLS12 {
				return nil, fmt.Errorf("Kafka TLS requires version 1.2 or newer")
			}
			if config.Net.TLS.Config.MaxVersion != 0 && config.Net.TLS.Config.MaxVersion < config.Net.TLS.Config.MinVersion {
				return nil, fmt.Errorf("Kafka TLS version range is invalid")
			}
			if config.Net.TLS.Config.RootCAs != nil {
				config.Net.TLS.Config.RootCAs = config.Net.TLS.Config.RootCAs.Clone()
			}
			if config.Net.TLS.Config.ClientCAs != nil {
				config.Net.TLS.Config.ClientCAs = config.Net.TLS.Config.ClientCAs.Clone()
			}
		}
	} else {
		// The deliberate tunnel opt-in is the only path permitting plaintext,
		// including SASL credentials. The caller must enforce the secure tunnel.
		config.Net.TLS.Config = nil
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid Kafka client configuration: %w", err)
	}
	return config, nil
}
