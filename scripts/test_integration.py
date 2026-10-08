#!/usr/bin/env python3
"""Run opt-in Kafka contracts against one disposable, loopback-only broker."""

import fcntl
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "apache/kafka@sha256:4ceccc577f03f51f6af8dbfda55194d0d892f4fa7913ffbded567ce3895622ed"


def command(args, timeout=30):
    """command executes an owned-resource operation within a bounded deadline."""
    return subprocess.run(args, check=True, text=True, capture_output=True, timeout=timeout)


def main():
    """main owns the validation lock, generated broker and cleanup for one run."""
    go = os.getenv("KAFKA_TEST_GO") or shutil.which("go")
    if not go:
        raise SystemExit("set KAFKA_TEST_GO to a qualified Go compiler")
    lock_path = os.getenv("KAFKA_TEST_LOCK", "/tmp/laisky-heavy-validation-20261008.lock")
    with open(lock_path, "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise SystemExit("heavy validation lock is occupied; no broker was started")
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        name = "go-kafka-test-" + uuid.uuid4().hex[:12]
        network = name + "-net"
        owned_network = False
        owned_container = False
        try:
            command(["docker", "network", "create", "--label", "go-kafka.integration=" + name, network])
            owned_network = True
            env = {
                "KAFKA_NODE_ID": "1", "KAFKA_PROCESS_ROLES": "broker,controller",
                "KAFKA_CONTROLLER_QUORUM_VOTERS": "1@broker:29093",
                "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "CONTROLLER:PLAINTEXT,INTERNAL:PLAINTEXT,EXTERNAL:PLAINTEXT",
                "KAFKA_LISTENERS": "CONTROLLER://:29093,INTERNAL://:19092,EXTERNAL://:9092",
                "KAFKA_ADVERTISED_LISTENERS": "INTERNAL://broker:19092,EXTERNAL://127.0.0.1:" + str(port),
                "KAFKA_INTER_BROKER_LISTENER_NAME": "INTERNAL",
                "KAFKA_CONTROLLER_LISTENER_NAMES": "CONTROLLER",
                "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1",
                "KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS": "0",
                "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR": "1",
                "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
                "KAFKA_LOG_DIRS": "/tmp/kraft-combined-logs",
                "KAFKA_HEAP_OPTS": "-Xms256m -Xmx512m",
            }
            args = ["docker", "run", "-d", "--pull=never", "--name", name, "--hostname", "broker",
                    "--network", network, "--label", "go-kafka.integration=" + name,
                    "--memory", "1g", "--cpus", "1", "-p", "127.0.0.1:%d:9092" % port]
            for key, value in env.items():
                args += ["-e", key + "=" + value]
            owned_container = True
            command(args + [IMAGE])
            deadline = time.monotonic() + 60
            while True:
                try:
                    command(["docker", "exec", name, "/opt/kafka/bin/kafka-topics.sh",
                             "--bootstrap-server", "broker:19092", "--list"], timeout=8)
                    break
                except (subprocess.SubprocessError, OSError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError("disposable Kafka did not become ready within 60 seconds")
                    time.sleep(1)
            with socket.create_connection(("127.0.0.1", port), timeout=3):
                pass
            print("Owned broker host-loopback readiness accepted", flush=True)
            test_env = {key: os.environ[key] for key in ("HOME", "PATH", "GOMODCACHE", "GOCACHE", "GOROOT", "GOPROXY", "GOSUMDB") if key in os.environ}
            test_env.update(KAFKA_TEST_BROKER="127.0.0.1:" + str(port), GOTOOLCHAIN="local")
            result = subprocess.run([go, "test", "-tags", "integration", "-race", "-count=1", "-timeout=120s", "-v", "./..."],
                                    cwd=ROOT, env=test_env, text=True, timeout=240)
            return result.returncode
        finally:
            if owned_container:
                identity = subprocess.run(["docker", "inspect", "-f", '{{index .Config.Labels "go-kafka.integration"}}', name], capture_output=True, text=True, timeout=10)
                if identity.returncode == 0 and identity.stdout.strip() == name:
                    subprocess.run(["docker", "rm", "-f", name], check=False, capture_output=True, timeout=30)
            if owned_network:
                subprocess.run(["docker", "network", "rm", network], check=False, capture_output=True, timeout=30)


if __name__ == "__main__":
    raise SystemExit(main())
