package compose

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]any            `yaml:"volumes"`
}

type composeService struct {
	Image string `yaml:"image"`
	Build struct {
		Context    string `yaml:"context"`
		Dockerfile string `yaml:"dockerfile"`
	} `yaml:"build"`
	Profiles    []string          `yaml:"profiles"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
	Command     any               `yaml:"command"`
	DependsOn   map[string]struct {
		Condition string `yaml:"condition"`
	} `yaml:"depends_on"`
	Healthcheck struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
}

func TestKafkaComposeContract(t *testing.T) {
	data, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}
	var file composeFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatalf("parse compose.yaml: %v", err)
	}

	kafka, ok := file.Services["kafka"]
	if !ok {
		t.Fatal("compose has no kafka service")
	}
	if kafka.Image != "apache/kafka:4.3.1" {
		t.Fatalf("Kafka image = %q, want pinned apache/kafka:4.3.1", kafka.Image)
	}
	if kafka.Environment["KAFKA_PROCESS_ROLES"] != "broker,controller" {
		t.Fatalf("Kafka process roles = %q", kafka.Environment["KAFKA_PROCESS_ROLES"])
	}
	if got := kafka.Environment["KAFKA_ADVERTISED_LISTENERS"]; !strings.Contains(got, "kafka:19092") || !strings.Contains(got, "localhost:59092") {
		t.Fatalf("Kafka advertised listeners do not cover container and host clients: %q", got)
	}
	if !contains(kafka.Ports, "127.0.0.1:59092:9092") {
		t.Fatalf("Kafka host port is not loopback-bound: %v", kafka.Ports)
	}
	if !contains(kafka.Volumes, "kafka-data:/var/lib/kafka/data") {
		t.Fatalf("Kafka data is not persistent: %v", kafka.Volumes)
	}
	if got := strings.Join(kafka.Healthcheck.Test, " "); !strings.Contains(got, "127.0.0.1:19092") {
		t.Fatalf("Kafka health check does not use the container-internal listener: %q", got)
	}
	if _, ok := file.Volumes["kafka-data"]; !ok {
		t.Fatal("kafka-data volume is not declared")
	}

	init, ok := file.Services["kafka-init"]
	if !ok {
		t.Fatal("compose has no kafka-init service")
	}
	if init.Image != kafka.Image {
		t.Fatalf("kafka-init image = %q, broker image = %q", init.Image, kafka.Image)
	}
	if init.DependsOn["kafka"].Condition != "service_healthy" {
		t.Fatalf("kafka-init dependency = %#v", init.DependsOn["kafka"])
	}
	if !contains(init.Volumes, "../kafka/init-topics.sh:/opt/nano/init-topics.sh:ro") {
		t.Fatalf("kafka-init does not mount the topic contract: %v", init.Volumes)
	}
}

func TestKafkaTopicContract(t *testing.T) {
	data, err := os.ReadFile("../kafka/init-topics.sh")
	if err != nil {
		t.Fatalf("read topic initializer: %v", err)
	}
	script := string(data)
	for _, topic := range []string{
		"nano.observability.agent-trace.v1",
		"nano.observability.agent-trace-purge.v1",
		"nano.observability.agent-trace-quarantine.v1",
		"nano.observability.otel-logs.v1",
		"nano.observability.otel-traces.v1",
	} {
		if !strings.Contains(script, topic) {
			t.Errorf("topic initializer is missing %s", topic)
		}
	}
	if !strings.Contains(script, "604800000") {
		t.Error("topic initializer does not pin the seven-day Kafka replay window")
	}
	if !strings.Contains(script, "min.insync.replicas") || !strings.Contains(script, "NANO_KAFKA_MIN_INSYNC_REPLICAS") {
		t.Error("topic initializer does not pin the application topic ISR requirement")
	}
	if !strings.Contains(script, "kafka-configs.sh") || !strings.Contains(script, "--alter") {
		t.Error("topic initializer does not update min ISR on existing application topics")
	}
}

func TestClickHouseComposeContract(t *testing.T) {
	data, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var file composeFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	clickhouse, ok := file.Services["clickhouse"]
	if !ok {
		t.Fatal("compose has no clickhouse service")
	}
	if clickhouse.Image != "clickhouse:26.3.17.56-jammy" {
		t.Fatalf("ClickHouse image=%q", clickhouse.Image)
	}
	if !contains(clickhouse.Ports, "127.0.0.1:58123:8123") || !contains(clickhouse.Ports, "127.0.0.1:59004:9000") {
		t.Fatalf("ClickHouse host ports are not loopback-bound: %v", clickhouse.Ports)
	}
	if !contains(clickhouse.Volumes, "clickhouse-data:/var/lib/clickhouse") {
		t.Fatalf("ClickHouse data is not persistent: %v", clickhouse.Volumes)
	}
	if _, ok := file.Volumes["clickhouse-data"]; !ok {
		t.Fatal("clickhouse-data volume is not declared")
	}
}

func TestDefaultTraceTopologyUsesKafkaAndClickHouse(t *testing.T) {
	data, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var file composeFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"observability-postgres", "agent-trace-processor-postgres"} {
		if _, ok := file.Services[retired]; ok {
			t.Errorf("compose still declares retired service %q", retired)
		}
	}
	if _, ok := file.Volumes["observability-postgres-data"]; ok {
		t.Error("compose still declares retired observability-postgres-data volume")
	}
	stageC, ok := file.Services["agent-trace-processor-clickhouse"]
	if !ok {
		t.Fatal("compose has no Stage C Agent Trace Processor")
	}
	if len(stageC.Profiles) != 0 ||
		stageC.Environment["NANO_CLICKHOUSE_ADDR"] != "clickhouse:9000" || stageC.DependsOn["clickhouse"].Condition != "service_healthy" {
		t.Fatalf("default ClickHouse processor=%#v", stageC)
	}
	if _, ok := stageC.Environment["NANO_AGENT_TRACE_PROCESSOR_STORE"]; ok {
		t.Error("Agent Trace Processor still exposes a storage backend selector")
	}
	if stageC.Build.Context != "../.." || stageC.Build.Dockerfile != "infra/agent-trace-processor/Dockerfile" ||
		stageC.DependsOn["kafka-init"].Condition != "service_completed_successfully" ||
		stageC.DependsOn["minio-init"].Condition != "service_completed_successfully" {
		t.Fatalf("ClickHouse processor dependencies/build=%#v", stageC)
	}
	collector := file.Services["collector"]
	if len(collector.Profiles) != 0 ||
		collector.DependsOn["clickhouse"].Condition != "service_healthy" {
		t.Fatalf("default Collector=%#v", collector)
	}
	if _, ok := collector.Environment["NANO_COLLECTOR_STORE"]; ok {
		t.Error("Collector still exposes a storage backend selector")
	}
	for key := range collector.Environment {
		if strings.HasPrefix(key, "NANO_COLLECTOR_DATABASE_") || strings.HasPrefix(key, "NANO_COLLECTOR_PROJECTION_DATABASE_") || strings.HasPrefix(key, "NANO_COLLECTOR_QUERY_DATABASE_") {
			t.Errorf("Collector still exposes retired PostgreSQL pool setting %s", key)
		}
	}
	if _, ok := collector.DependsOn["observability-postgres"]; ok {
		t.Fatal("default Collector still depends on Observability PostgreSQL")
	}
	if got := strings.Join(collector.Healthcheck.Test, " "); !strings.Contains(got, "127.0.0.1:9093/metrics") {
		t.Fatalf("default Collector health does not cover its metrics listener: %q", got)
	}
}

func TestProductionTraceTopologyUsesKafkaAndClickHouse(t *testing.T) {
	data, err := os.ReadFile("compose.prod.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var file composeFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"control-plane", "worker"} {
		service := file.Services[name]
		if _, found := service.Environment["NANO_AGENT_TRACE_TRANSPORT"]; found {
			t.Fatalf("%s still exposes Trace transport selection", name)
		}
		if service.Environment["NANO_AGENT_TRACE_KAFKA_BROKERS"] != "kafka-1:19092,kafka-2:19092,kafka-3:19092" {
			t.Fatalf("%s is not a Kafka Trace producer: %#v", name, service.Environment)
		}
	}
	for _, name := range []string{"kafka-1", "kafka-2", "kafka-3"} {
		broker, ok := file.Services[name]
		if !ok || broker.Environment["KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR"] != "3" ||
			broker.Environment["KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR"] != "3" {
			t.Fatalf("production broker %s is not RF=3: %#v", name, broker)
		}
	}
	init := file.Services["kafka-init"]
	if init.Environment["NANO_KAFKA_REPLICATION_FACTOR"] != "3" {
		t.Fatalf("production topic replication factor = %q", init.Environment["NANO_KAFKA_REPLICATION_FACTOR"])
	}
	if init.Environment["NANO_KAFKA_MIN_INSYNC_REPLICAS"] != "2" {
		t.Fatalf("production topic min ISR = %q", init.Environment["NANO_KAFKA_MIN_INSYNC_REPLICAS"])
	}
	processor, ok := file.Services["agent-trace-processor-clickhouse"]
	if !ok ||
		processor.DependsOn["kafka-init"].Condition != "service_completed_successfully" ||
		processor.DependsOn["clickhouse"].Condition != "service_healthy" {
		t.Fatalf("production ClickHouse processor=%#v", processor)
	}
	collector := file.Services["collector"]
	if collector.DependsOn["clickhouse"].Condition != "service_healthy" {
		t.Fatalf("production Collector=%#v", collector)
	}
	for serviceName, service := range map[string]composeService{"Agent Trace Processor": processor, "Collector": collector} {
		for _, selector := range []string{"NANO_AGENT_TRACE_PROCESSOR_STORE", "NANO_COLLECTOR_STORE"} {
			if _, ok := service.Environment[selector]; ok {
				t.Errorf("production %s still exposes %s", serviceName, selector)
			}
		}
	}
	for key := range collector.Environment {
		if strings.HasPrefix(key, "NANO_COLLECTOR_DATABASE_") || strings.HasPrefix(key, "NANO_COLLECTOR_PROJECTION_DATABASE_") || strings.HasPrefix(key, "NANO_COLLECTOR_QUERY_DATABASE_") {
			t.Errorf("production Collector still exposes retired PostgreSQL pool setting %s", key)
		}
	}
	if got := strings.Join(collector.Healthcheck.Test, " "); !strings.Contains(got, "127.0.0.1:9093/metrics") {
		t.Fatalf("production Collector health does not cover its metrics listener: %q", got)
	}
	if _, ok := collector.DependsOn["observability-postgres"]; ok {
		t.Fatal("production Collector still depends on Observability PostgreSQL")
	}
	if _, ok := file.Services["observability-postgres"]; ok {
		t.Error("production Compose still declares retired Observability PostgreSQL")
	}
	if _, ok := file.Volumes["observability-postgres-data"]; ok {
		t.Error("production Compose still declares retired observability-postgres-data volume")
	}
}

func TestWebReaderConcurrencyContract(t *testing.T) {
	for _, name := range []string{"compose.yaml", "compose.prod.yaml"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var file composeFile
		if err := yaml.Unmarshal(data, &file); err != nil {
			t.Fatal(err)
		}
		reader := file.Services["web-reader"]
		if reader.Environment["NANO_WEB_READER_MAX_CONCURRENT"] != "8" ||
			reader.Environment["NANO_WEB_READER_BROWSER_MAX_CONCURRENT"] != "4" {
			t.Fatalf("%s web-reader concurrency=%q/%q, want 8/4", name,
				reader.Environment["NANO_WEB_READER_MAX_CONCURRENT"],
				reader.Environment["NANO_WEB_READER_BROWSER_MAX_CONCURRENT"])
		}
	}
}

func TestStartAndGoGateUseDefaultClickHouseTraceTopology(t *testing.T) {
	start, err := os.ReadFile("../../scripts/start")
	if err != nil {
		t.Fatal(err)
	}
	startScript := string(start)
	for _, required := range []string{"NANO_AGENT_TRACE_KAFKA_BROKERS", "NANO_COLLECTOR_URL", "agent-trace-processor-clickhouse", "collector"} {
		if !strings.Contains(startScript, required) {
			t.Errorf("scripts/start is missing %s", required)
		}
	}
	if strings.Contains(startScript, "NANO_AGENT_TRACE_TRANSPORT") {
		t.Error("scripts/start still exposes removed Trace transport selection")
	}
	if strings.Contains(startScript, "scripts/prepare-observability-db") || strings.Contains(startScript, "go run ./cmd/collector") {
		t.Error("scripts/start still starts the PostgreSQL-era Collector path")
	}
	testGo, err := os.ReadFile("../../scripts/test-go")
	if err != nil {
		t.Fatal(err)
	}
	testScript := string(testGo)
	if !strings.Contains(testScript, "NANO_TEST_CLICKHOUSE_ADDR") || !strings.Contains(testScript, "clickhouse") {
		t.Error("scripts/test-go does not provision and exercise ClickHouse")
	}
	for _, scriptName := range []string{"start", "migrate", "health", "bootstrap", "test-go", "test-collector-service"} {
		contents, err := os.ReadFile("../../scripts/" + scriptName)
		if err != nil {
			t.Fatal(err)
		}
		for _, retired := range []string{"observability-postgres", "NANO_COLLECTOR_DATABASE_URL", "NANO_TEST_OBSERVABILITY_DATABASE_URL", "cmd/collector-rebuild", "prepare-observability-db"} {
			if strings.Contains(string(contents), retired) {
				t.Errorf("scripts/%s still references retired %s", scriptName, retired)
			}
		}
	}
	health, err := os.ReadFile("../../scripts/health")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(health), "127.0.0.1:58082/internal/agent-observability/v1/health") {
		t.Error("scripts/health does not probe the Compose-published Collector endpoint")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestProductionBifrostMountsInternationalConfig(t *testing.T) {
	data, err := os.ReadFile("compose.prod.yaml")
	if err != nil {
		t.Fatalf("read compose.prod.yaml: %v", err)
	}
	var file composeFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatalf("parse compose.prod.yaml: %v", err)
	}
	bifrost := file.Services["bifrost"]
	if !contains(bifrost.Volumes, "../bifrost/config.prod.json:/app/data/config.json:ro") {
		t.Fatalf("production Bifrost does not mount config.prod.json: %v", bifrost.Volumes)
	}
}
