#!/usr/bin/env python3
"""Run generated TLS/mTLS/SASL contracts in one offline, disposable Kafka namespace."""
import pathlib, subprocess, os, json, time, fcntl, shutil, tempfile, uuid

KAFKA_IMAGE = "apache/kafka@sha256:4ceccc577f03f51f6af8dbfda55194d0d892f4fa7913ffbded567ce3895622ed"
GO_IMAGE = "golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c"

def main():
 p=pathlib.Path(__file__).resolve().parents[1]
 label="go-kafka-secure-"+uuid.uuid4().hex
 lock=open(os.getenv("KAFKA_TEST_LOCK", "/tmp/laisky-heavy-validation-20261008.lock"), "a")
 try: fcntl.flock(lock, fcntl.LOCK_EX|fcntl.LOCK_NB)
 except BlockingIOError:
  print("ADMISSION_BUSY", flush=True)
  return 75
 with tempfile.TemporaryDirectory(prefix="go-kafka-secure-") as temp:
  r=pathlib.Path(temp)
  material=r/"kafka-synthetic-tls";assert not material.exists();material.mkdir()
  def run(args,cwd=material):subprocess.run(args,cwd=cwd,capture_output=True,check=True,timeout=60)
  run(["openssl","req","-x509","-newkey","rsa:2048","-nodes","-keyout","ca.key","-out","ca.crt","-subj","/CN=Synthetic Test CA","-days","1"])
  for name in ["server","client"]:
   run(["openssl","req","-newkey","rsa:2048","-nodes","-keyout",name+".key","-out",name+".csr","-subj","/CN="+("localhost" if name=="server" else "Synthetic Test Client")])
   ext=material/(name+".ext")
   ext.write_text("subjectAltName=DNS:localhost,IP:127.0.0.1"+chr(10)+"extendedKeyUsage="+("serverAuth" if name=="server" else "clientAuth")+chr(10))
   run(["openssl","x509","-req","-in",name+".csr","-CA","ca.crt","-CAkey","ca.key","-CAcreateserial","-out",name+".crt","-days","1","-extfile",name+".ext"])
  run(["openssl","pkcs12","-export","-in","server.crt","-inkey","server.key","-certfile","ca.crt","-out","server.p12","-passout","pass:synthetic-only"])
  run(["docker","run","--rm","--network","none","--cpus","0.25","--memory","256m","--user",str(os.getuid())+":"+str(os.getgid()),"--mount",f"type=bind,src={material},dst=/synthetic","--entrypoint","bash",KAFKA_IMAGE,"-ec","exec keytool -importcert -noprompt -alias synthetic-ca -file /synthetic/ca.crt -keystore /synthetic/trust.p12 -storetype PKCS12 -storepass synthetic-only"])
  for f in material.iterdir():f.chmod(0o644)
  config=r/"kafka-secure.properties"
  config.write_text("""process.roles=broker,controller
  node.id=1
  controller.quorum.voters=1@localhost:9094
  listeners=INTERNAL://127.0.0.1:19092,SASL_SSL://127.0.0.1:9093,CONTROLLER://127.0.0.1:9094
  advertised.listeners=INTERNAL://localhost:19092,SASL_SSL://localhost:9093
  listener.security.protocol.map=INTERNAL:PLAINTEXT,SASL_SSL:SASL_SSL,CONTROLLER:PLAINTEXT
  controller.listener.names=CONTROLLER
  inter.broker.listener.name=INTERNAL
  log.dirs=/tmp/kafka-data
  offsets.topic.replication.factor=1
  transaction.state.log.replication.factor=1
  transaction.state.log.min.isr=1
  group.initial.rebalance.delay.ms=0
  auto.create.topics.enable=true
  ssl.keystore.location=/synthetic/server.p12
  ssl.keystore.type=PKCS12
  ssl.keystore.password=synthetic-only
  ssl.key.password=synthetic-only
  ssl.truststore.location=/synthetic/trust.p12
  ssl.truststore.type=PKCS12
  ssl.truststore.password=synthetic-only
  listener.name.sasl_ssl.ssl.client.auth=required
  sasl.enabled.mechanisms=PLAIN
  listener.name.sasl_ssl.plain.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username="synthetic-user" password="synthetic-only" user_synthetic-user="synthetic-only";
  """)
  admin=r/"kafka-secure-admin.properties"
  admin.write_text("request.timeout.ms=3000"+chr(10)+"default.api.timeout.ms=5000"+chr(10))
  ctx=r/"kafka-secure-source";assert not ctx.exists();ctx.mkdir()
  for name in subprocess.check_output(["git","ls-files"],cwd=p,text=True).splitlines()+["transport.go","transport_security_test.go","transport_tls_test.go"]:
   src=p/name
   if src.is_file() and (name.endswith(".go") or name in ["go.mod","go.sum"]):
    dest=ctx/name;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(src,dest)
  common=["--label","task="+label,"--cap-drop","ALL","--security-opt","no-new-privileges:true"]
  start="/opt/kafka/bin/kafka-storage.sh format --cluster-id MkU3OEVBNTcwNTJENDM2Qk --config /qualification/server.properties && exec /opt/kafka/bin/kafka-server-start.sh /qualification/server.properties"
  q=subprocess.run(["docker","run","-d",*common,"--network","none","--cpus","0.75","--memory","1200m","--tmpfs","/tmp:size=1g,mode=1777,exec","--mount",f"type=bind,src={material},dst=/synthetic,readonly","--mount",f"type=bind,src={config},dst=/qualification/server.properties,readonly","--mount",f"type=bind,src={admin},dst=/qualification/admin.properties,readonly","-e","KAFKA_HEAP_OPTS=-Xms256m -Xmx512m","--entrypoint","bash",KAFKA_IMAGE,"-ec",start],capture_output=True,text=True,check=True);cid=q.stdout.strip()
  try:
   ready=False
   for i in range(12):
    try:
     q=subprocess.run(["docker","exec",cid,"/opt/kafka/bin/kafka-topics.sh","--bootstrap-server","localhost:19092","--list","--command-config","/qualification/admin.properties"],capture_output=True,text=True,timeout=20)
     if q.returncode==0:ready=True;break
    except subprocess.TimeoutExpired:
     pass
    time.sleep(1)
   if not ready:
    q=subprocess.run(["docker","logs",cid],capture_output=True,text=True)
    (r/"kafka-secure-broker.log").write_text(q.stdout+q.stderr);raise RuntimeError("owned secure broker not ready")
   cache=subprocess.check_output([os.getenv("KAFKA_TEST_GO") or shutil.which("go"),"env","GOCACHE","GOMODCACHE"],cwd=p,text=True).splitlines()
   args=["docker","run","--rm",*common,"--network","container:"+cid,"--cpus","1.25","--memory","2g","--user",str(os.getuid())+":"+str(os.getgid()),"--read-only","--tmpfs","/tmp:size=1g,mode=1777,exec","--mount",f"type=bind,src={ctx},dst=/source,readonly","--mount",f"type=bind,src={material},dst=/synthetic,readonly"]
   for c in cache:args+=["--mount",f"type=bind,src={c},dst={c}"]
   for v in ["HOME=/tmp","GOTOOLCHAIN=local","GOMAXPROCS=2","GOPROXY=off","GOSUMDB=off","GOCACHE="+cache[0],"GOMODCACHE="+cache[1],"KAFKA_TEST_BROKER=127.0.0.1:9093","KAFKA_TEST_TLS_CA=/synthetic/ca.crt","KAFKA_TEST_TLS_CERT=/synthetic/client.crt","KAFKA_TEST_TLS_KEY=/synthetic/client.key","KAFKA_TEST_SASL_USER=synthetic-user","KAFKA_TEST_SASL_PASSWORD=synthetic-only"]:args+=["-e",v]
   args+=["-w","/source",GO_IMAGE,"go","test","-mod=readonly","-p=1","-tags","integration","-race","-count=1","-timeout=3m","-v","./..."]
   log=r/"kafka-secure-acceptance.log";started=time.time()
   with log.open("w") as f:q=subprocess.run(args,stdout=f,stderr=subprocess.STDOUT,timeout=240)
   receipt={"code":q.returncode,"seconds":time.time()-started,"fixture":cid,"source":str(ctx)}
   (r/"kafka-secure-acceptance.json").write_text(json.dumps(receipt,indent=2));print(json.dumps(receipt),flush=True);print(log.read_text(errors="replace"),flush=True)
  finally:
   brokerLog=subprocess.run(["docker","logs",cid],capture_output=True,text=True)
   (r/"kafka-secure-broker.log").write_text(brokerLog.stdout+brokerLog.stderr)
   d=json.loads(subprocess.check_output(["docker","inspect",cid],text=True))[0];assert d["Id"]==cid and d["Config"]["Labels"].get("task")==label
   subprocess.run(["docker","stop","--time","5",cid],capture_output=True,check=True);subprocess.run(["docker","rm",cid],capture_output=True,check=True)
   print("OWN_SECURE_FIXTURE_REMOVED",cid[:12],flush=True)

  return receipt["code"]

if __name__ == "__main__":
 raise SystemExit(main())
