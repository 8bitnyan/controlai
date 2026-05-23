// Package main is the controlai-ingest binary that runs inside every site
// container. It is configured exclusively by environment variables and a
// mounted site.yaml snapshot.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	autopaho "github.com/eclipse/paho.golang/autopaho"
	pahopkg "github.com/eclipse/paho.golang/paho"
	cbor "github.com/fxamacker/cbor/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// config holds the ingest runtime configuration loaded from environment.
type ingestConfig struct {
	TenantID        string
	SiteID          string
	BrokerKind      string
	MQTTBroker      string // host:port
	MQTTTopic       string
	PayloadCodec    string
	BatchSize       int
	FlushIntervalMS int
	Direction       string // uni | bi
	TSDBHost        string
	TSDBPort        string
	TSDBDB          string
	TSDBUser        string
	TSDBPass        string
	CAPath          string
	CertPath        string
	KeyPath         string
}

func loadConfig() ingestConfig {
	batchSize, _ := strconv.Atoi(envDefault("INGEST_BATCH_SIZE", "200"))
	flushMS, _ := strconv.Atoi(envDefault("INGEST_FLUSH_INTERVAL_MS", "1000"))
	return ingestConfig{
		TenantID:        envDefault("INGEST_TENANT_ID", ""),
		SiteID:          envDefault("INGEST_SITE_ID", ""),
		BrokerKind:      envDefault("INGEST_BROKER_KIND", "mosquitto"),
		MQTTBroker:      envDefault("INGEST_MQTT_BROKER", "localhost:8883"),
		MQTTTopic:       envDefault("INGEST_MQTT_TOPIC", "#"),
		PayloadCodec:    envDefault("INGEST_PAYLOAD_CODEC", "cbor"),
		BatchSize:       batchSize,
		FlushIntervalMS: flushMS,
		Direction:       envDefault("INGEST_DIRECTION", "uni"),
		TSDBHost:        envDefault("INGEST_TSDB_HOST", "localhost"),
		TSDBPort:        envDefault("INGEST_TSDB_PORT", "5432"),
		TSDBDB:          envDefault("INGEST_TSDB_DB", "telemetry"),
		TSDBUser:        envDefault("INGEST_TSDB_USER", "controlai_ingest"),
		TSDBPass:        envDefault("INGEST_TSDB_PASS", ""),
		CAPath:          envDefault("INGEST_CA_PATH", "/certs/ca.crt"),
		CertPath:        envDefault("INGEST_CERT_PATH", "/certs/client.crt"),
		KeyPath:         envDefault("INGEST_KEY_PATH", "/certs/client.key"),
	}
}

// telemetryRow is one message to be inserted into TimescaleDB.
type telemetryRow struct {
	Time     time.Time
	SiteID   string
	DeviceID string
	Metric   string
	Payload  []byte // JSON bytes or nil
	Raw      []byte // original MQTT payload
	IsJSON   bool   // true → Payload goes into jsonb column; false → raw only
}

var (
	ingestLog = slog.Default()
	ingestCfg ingestConfig
	ingestDB  *pgxpool.Pool
	ringMu    sync.Mutex
	ring      []telemetryRow
)

func main() {
	ingestCfg = loadConfig()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Connect to TSDB.
	dsn := fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable",
		ingestCfg.TSDBHost, ingestCfg.TSDBPort, ingestCfg.TSDBDB, ingestCfg.TSDBUser, ingestCfg.TSDBPass)
	var err error
	ingestDB, err = pgxpool.New(ctx, dsn)
	if err != nil {
		ingestLog.Error("connect to TSDB", "err", err)
		os.Exit(1)
	}
	defer ingestDB.Close()

	// Build mTLS config.
	tlsCfg, err := buildTLS(ingestCfg)
	if err != nil {
		ingestLog.Error("build TLS config", "err", err)
		os.Exit(1)
	}

	// Start flush ticker.
	flushTicker := time.NewTicker(time.Duration(ingestCfg.FlushIntervalMS) * time.Millisecond)
	defer flushTicker.Stop()
	go flushLoop(ctx, flushTicker)

	// Start downlink HTTP server if bi mode.
	if ingestCfg.Direction == "bi" {
		go serveDownlink(ctx)
	}

	// Connect to MQTT broker.
	brokerURL := mustParseURL("mqtts://" + ingestCfg.MQTTBroker)
	pahoConnCfg := autopaho.ClientConfig{
		BrokerUrls:        []*url.URL{brokerURL},
		TlsCfg:            tlsCfg,
		KeepAlive:         30,
		ConnectRetryDelay: 5 * time.Second,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, connAck *pahopkg.Connack) {
			ingestLog.Info("MQTT connected", "broker", ingestCfg.MQTTBroker)
			if _, err := cm.Subscribe(ctx, &pahopkg.Subscribe{
				Subscriptions: []pahopkg.SubscribeOptions{
					{Topic: ingestCfg.MQTTTopic, QoS: 1},
				},
			}); err != nil {
				ingestLog.Error("MQTT subscribe", "err", err)
			}
		},
		OnConnectError: func(err error) {
			ingestLog.Warn("MQTT connect error", "err", err)
		},
		ClientConfig: pahopkg.ClientConfig{
			ClientID: fmt.Sprintf("controlai-ingest-%s-%s", ingestCfg.TenantID, ingestCfg.SiteID),
			Router: pahopkg.NewSingleHandlerRouter(func(m *pahopkg.Publish) {
				handleMessage(m)
			}),
		},
	}

	cm, err := autopaho.NewConnection(ctx, pahoConnCfg)
	if err != nil {
		ingestLog.Error("create MQTT connection manager", "err", err)
		os.Exit(1)
	}

	// Wait for shutdown signal.
	<-ctx.Done()
	ingestLog.Info("SIGTERM received; draining ring buffer...")
	// Give a 10-second drain budget.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer drainCancel()
	_ = cm.Disconnect(drainCtx)
	flushNow(drainCtx)
	ingestDB.Close()
	ingestLog.Info("ingest shut down cleanly")
}

// handleMessage decodes and buffers an MQTT message.
func handleMessage(m *pahopkg.Publish) {
	parts := strings.Split(m.Topic, "/")
	deviceID, metric := "", ""
	if len(parts) >= 4 {
		deviceID = parts[2]
		metric = parts[3]
	}
	row := telemetryRow{
		Time:     time.Now().UTC(),
		SiteID:   ingestCfg.SiteID,
		DeviceID: deviceID,
		Metric:   metric,
		Raw:      m.Payload,
	}
	switch ingestCfg.PayloadCodec {
	case "cbor":
		var decoded map[string]any
		if err := cbor.Unmarshal(m.Payload, &decoded); err != nil {
			ingestLog.Warn("CBOR decode failed; storing raw only", "err", err)
		} else {
			b, _ := json.Marshal(decoded)
			row.Payload = b
			row.IsJSON = true
		}
	case "json":
		var decoded map[string]any
		if err := json.Unmarshal(m.Payload, &decoded); err != nil {
			ingestLog.Warn("JSON decode failed; storing raw only", "err", err)
		} else {
			row.Payload = m.Payload
			row.IsJSON = true
		}
	case "raw_passthrough":
		// payload column is NULL; raw bytea only.
	}

	ringMu.Lock()
	ring = append(ring, row)
	size := len(ring)
	batchSz := ingestCfg.BatchSize
	ringMu.Unlock()

	if size >= batchSz {
		flushNow(context.Background())
	}
}

// flushLoop flushes on the ticker interval.
func flushLoop(ctx context.Context, t *time.Ticker) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			flushNow(ctx)
		}
	}
}

// flushNow drains the ring buffer into TimescaleDB with a multi-row INSERT.
func flushNow(ctx context.Context) {
	ringMu.Lock()
	if len(ring) == 0 {
		ringMu.Unlock()
		return
	}
	batch := ring
	ring = nil
	ringMu.Unlock()

	const baseQ = `INSERT INTO telemetry (time, site_id, device_id, metric, payload, raw) VALUES `
	args := make([]any, 0, len(batch)*6)
	placeholders := make([]string, 0, len(batch))
	for i, r := range batch {
		base := i * 6
		var payloadArg any
		if r.IsJSON && r.Payload != nil {
			payloadArg = string(r.Payload)
		}
		args = append(args, r.Time, r.SiteID, r.DeviceID, r.Metric, payloadArg, r.Raw)
		placeholders = append(placeholders, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d::jsonb,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6))
	}
	q := baseQ + strings.Join(placeholders, ",")
	if _, err := ingestDB.Exec(ctx, q, args...); err != nil {
		ingestLog.Error("flush to TSDB failed", "rows", len(batch), "err", err)
		// Re-queue on failure — best-effort.
		ringMu.Lock()
		ring = append(batch, ring...)
		ringMu.Unlock()
	} else {
		ingestLog.Debug("flushed batch", "rows", len(batch))
	}
}

// serveDownlink starts an HTTP server for bi-mode downlink commands (internal).
func serveDownlink(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/publish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// In MVP the daemon publishes directly to MQTT; this endpoint is reserved.
		w.WriteHeader(http.StatusAccepted)
	})
	srv := &http.Server{Addr: ":9000", Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		ingestLog.Error("downlink server", "err", err)
	}
}

// buildTLS constructs a tls.Config with the site's mTLS certificates.
func buildTLS(c ingestConfig) (*tls.Config, error) {
	caPEM, err := os.ReadFile(c.CAPath)
	if err != nil {
		return nil, fmt.Errorf("read CA %s: %w", c.CAPath, err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse CA cert")
	}
	cert, err := tls.LoadX509KeyPair(c.CertPath, c.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load client cert: %w", err)
	}
	return &tls.Config{
		RootCAs:      caPool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}
