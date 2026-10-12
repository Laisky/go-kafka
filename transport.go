package kafka

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	cluster "github.com/bsm/sarama-cluster"
)

func validatedKafkaConfig(cfg *KafkaCliCfg) (*cluster.Config, error) {
	if cfg == nil || len(cfg.Brokers) == 0 || len(cfg.Topics) == 0 || cfg.Groupid == "" || cfg.KMsgPool == nil {
		return nil, fmt.Errorf("brokers, topics, group ID and message pool are required")
	}
	for _, broker := range cfg.Brokers {
		if broker == "" || strings.Contains(broker, "@") {
			return nil, fmt.Errorf("invalid Kafka broker address")
		}
	}
	config := cluster.NewConfig()
	if cfg.ClusterConfig != nil {
		*config = *cfg.ClusterConfig
		config.Group.Member.UserData = append([]byte(nil), cfg.ClusterConfig.Group.Member.UserData...)
	}
	config.Net.KeepAlive = 30 * time.Second
	config.Consumer.Return.Errors = true
	config.Group.Return.Notifications = true
	config.Consumer.Offsets.CommitInterval = time.Second
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
