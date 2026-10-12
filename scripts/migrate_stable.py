#!/usr/bin/env python3
"""One-time, reviewable migration of the legacy dependency APIs on the PR branch."""
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]

def edit(name, replacements):
    path = ROOT / name
    text = path.read_text()
    for old, new in replacements:
        if old not in text:
            raise RuntimeError(f"missing migration anchor in {name}: {old!r}")
        text = text.replace(old, new)
    path.write_text(text)


def main():
    edit("commit_filter.go", [
        ('utils "github.com/Laisky/go-utils"', 'glog "github.com/Laisky/go-utils/v6/log"\n\t"github.com/spf13/viper"'),
        ('utils.Logger', 'glog.Shared'),
        ('utils.Clock.GetUTCNow()', 'time.Now().UTC()'),
        ('utils.Settings.GetBool', 'viper.GetBool'),
        ('partition int32\n}', 'partition int32\n\tclaim *claimState\n}'),
        ('partition: kmsg.Partition}', 'partition: kmsg.Partition, claim: kmsg.claim}'),
        ('for _, optf := range opts {', 'if kMsgPool == nil { return nil, errors.New("message pool is required") }\n\tfor _, optf := range opts {\n\t\tif optf == nil { return nil, errors.New("nil commit option") }'),
        ('glog.Shared.Info("NewCommitFilter",', 'if f.commitCheckInterval <= 0 || f.commitCheckNum < 0 || f.commitCheckChanSize < 0 {\n\t\treturn nil, errors.New("commit interval must be positive and count/queue size nonnegative")\n\t}\n\tglog.Shared.Info("NewCommitFilter",'),
        ('// Offsets are comparable only within the same topic and partition.', 'if kmsg == nil { continue }\n\t\tif kmsg.claim != nil && !kmsg.claim.live() { f.kMsgPool.Put(kmsg); continue }\n\t\t// Offsets are comparable only within one topic, partition and assignment.'),
        ('for _, record := range kmsgSlots {', 'for key, record := range kmsgSlots {\n\t\tif key.claim != nil && !key.claim.live() {\n\t\t\tif !record.isCommitted { f.kMsgPool.Put(record.lastMsg) }\n\t\t\tdelete(kmsgSlots, key)\n\t\t\tcontinue\n\t\t}'),
    ])
    edit("transport.go", [
        ('cluster "github.com/bsm/sarama-cluster"', '"github.com/IBM/sarama"'),
        ('*cluster.Config', '*sarama.Config'),
        ('cluster.NewConfig()', 'sarama.NewConfig()'),
        ('config.Group.Member.UserData = append([]byte(nil), cfg.ClusterConfig.Group.Member.UserData...)', 'config.Consumer.Group.Member.UserData = append([]byte(nil), cfg.ClusterConfig.Consumer.Group.Member.UserData...)\n\t\tconfig.Consumer.Group.Rebalance.GroupStrategies = append([]sarama.BalanceStrategy(nil), cfg.ClusterConfig.Consumer.Group.Rebalance.GroupStrategies...)'),
        ('// The wrapper exposes only the multiplexed Messages stream, not Partitions.\n\t// Accepting partition mode would silently strand messages in an unread stream.\n\tif config.Group.Mode != cluster.ConsumerModeMultiplex {\n\t\treturn nil, fmt.Errorf("Kafka wrapper requires ConsumerModeMultiplex")\n\t}', '// Preserve the original eager-rebalance contract. Cooperative claims may be\n\t// revoked without cancelling the session, which a backpressured channel API\n\t// cannot use as its shutdown signal. Reject unsupported strategies before dial.\n\tstrategies := config.Consumer.Group.Rebalance.GroupStrategies\n\tif legacy := config.Consumer.Group.Rebalance.Strategy; legacy != nil { strategies = append(append([]sarama.BalanceStrategy(nil), strategies...), legacy) }\n\tfor _, strategy := range strategies {\n\t\tif strategy == nil { return nil, fmt.Errorf("nil Kafka balance strategy") }\n\t\tswitch strategy.Name() {\n\t\tcase "range", "roundrobin", "sticky":\n\t\tdefault: return nil, fmt.Errorf("Kafka wrapper requires an eager balance strategy, got %q", strategy.Name())\n\t\t}\n\t}'),
        ('\tconfig.Group.Return.Notifications = true\n', ''),
        ('config.Consumer.Offsets.CommitInterval', 'config.Consumer.Offsets.AutoCommit.Interval'),
    ])
    for name in ["transport_security_test.go", "transport_tls_test.go"]:
        edit(name, [('cluster "github.com/bsm/sarama-cluster"', '"github.com/IBM/sarama"'), ('cluster.NewConfig()', 'sarama.NewConfig()')])
    edit("client_integration_test.go", [
        ('\tcluster "github.com/bsm/sarama-cluster"\n', ''),
        ('github.com/Shopify/sarama', 'github.com/IBM/sarama'),
        ('clusterCfg := cluster.NewConfig()\n\tclusterCfg.Config = *integrationConfig()', 'clusterCfg := integrationConfig()'),
        ('cfg := cluster.NewConfig()\n\t\t\tcfg.Config = *integrationConfig()', 'cfg := integrationConfig()'),
        ('cfg.Version = sarama.V0_10_2_0', 'cfg.Version = sarama.V2_8_0_0'),
    ])
    edit("tests/dependencysecurity/dependencies_test.go", [
        ('utils "github.com/Laisky/go-utils"', '"errors"'),
        ('github.com/golang-jwt/jwt/v4', 'github.com/golang-jwt/jwt/v5'),
        ('if err := utils.Settings.LoadFromFile(path); err != nil {', 'viper.SetConfigFile(path)\n\tif err := viper.ReadInConfig(); err != nil {'),
        ('utils.Settings.', 'viper.'),
        ('validation, ok := err.(*jwt.ValidationError)\n\tif err == nil || !ok || validation.Errors&jwt.ValidationErrorSignatureInvalid == 0 || validation.Errors&jwt.ValidationErrorExpired != 0 || (token != nil && token.Valid) {', 'if err == nil || !errors.Is(err, jwt.ErrTokenSignatureInvalid) || errors.Is(err, jwt.ErrTokenExpired) || (token != nil && token.Valid) {'),
        ('JWT v4 fixed behavior', 'JWT v5 fixed behavior'),
    ])
    # Legacy partition mode is absent from the native group API. Retain explicit
    # rejection coverage for the native API's unsupported lifecycle mode instead.
    (ROOT / "transport_mode_test.go").write_text('''package kafka
import (
    "testing"
    "github.com/IBM/sarama"
)
type namedTestStrategy struct { sarama.BalanceStrategy; name string }
func (s namedTestStrategy) Name() string { return s.name }
func TestTransportRejectsUnsupportedConsumerModes(t *testing.T) {
    for _, name := range []string{"cooperative-sticky", "unknown"} {
        cfg := validWrapperConfig()
        cfg.ClusterConfig = sarama.NewConfig()
        cfg.ClusterConfig.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{namedTestStrategy{name: name}}
        if _, err := validatedKafkaConfig(cfg); err == nil { t.Fatalf("unsupported lifecycle mode %s accepted", name) }
        if cfg.ClusterConfig.Consumer.Group.Rebalance.GroupStrategies[0].Name() != name { t.Fatal("caller configuration mutated") }
    }
}
func TestTransportCopiesNativeMemberData(t *testing.T) {
    cfg := validWrapperConfig()
    cfg.ClusterConfig = sarama.NewConfig()
    cfg.ClusterConfig.Consumer.Group.Member.UserData = []byte("original")
    effective, err := validatedKafkaConfig(cfg)
    if err != nil { t.Fatal(err) }
    effective.Consumer.Group.Member.UserData[0] = 'X'
    if string(cfg.ClusterConfig.Consumer.Group.Member.UserData) != "original" { t.Fatal("member metadata aliases caller") }
}
''')
    # Every surviving Go import is resolved through the current release graph.
    (ROOT / "go.mod").write_text("module github.com/Laisky/go-kafka\n\ngo 1.26.9\n")
    modules = [
        "github.com/IBM/sarama", "github.com/Laisky/go-utils/v6",
        "github.com/golang-jwt/jwt/v5", "github.com/hashicorp/consul/api",
        "github.com/hashicorp/go-retryablehttp", "github.com/klauspost/compress",
        "github.com/pkg/errors", "github.com/pkg/sftp", "github.com/prometheus/client_golang",
        "github.com/sirupsen/logrus", "github.com/spf13/pflag", "github.com/spf13/viper",
        "google.golang.org/protobuf",
    ]
    subprocess.run(["go", "get", *[m + "@latest" for m in modules]], cwd=ROOT, check=True)
    subprocess.run(["go", "get", "-u", "-t", "./..."], cwd=ROOT, check=True)
    subprocess.run(["go", "mod", "tidy"], cwd=ROOT, check=True)
    for name in [".github/workflows/test.yml", ".github/workflows/security-review.yml"]:
        edit(name, [("'1.26.9'", "'1.27.2'")])
    # Resolve the fully versioned official Go image to an immutable digest once.
    subprocess.run(["docker", "pull", "golang:1.27.2"], check=True)
    digest = subprocess.check_output(["docker", "image", "inspect", "--format", "{{index .RepoDigests 0}}", "golang:1.27.2"], text=True).strip()
    if not re.fullmatch(r"golang@sha256:[0-9a-f]{64}", digest):
        raise RuntimeError("unexpected official Go image identity")
    path = ROOT / "scripts/test_secure_integration.py"
    text = re.sub(r'GO_IMAGE = "[^"]+"', 'GO_IMAGE = "' + digest + '"', path.read_text())
    # Keep the full secure acceptance log, including observed broker offsets.
    text = text.replace('log.read_text(errors="replace")[-6000:]', 'log.read_text(errors="replace")')
    path.write_text(text)
    subprocess.run(["gofmt", "-w", *map(str, ROOT.glob("*.go")), *map(str, (ROOT/"tests").rglob("*.go"))], check=True)

if __name__ == "__main__":
    main()
