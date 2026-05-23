package config

import (
	"fmt"
	"regexp"

	"controlai/internal/version"
)

// BrokerKind identifies the MQTT broker implementation.
type BrokerKind string

const (
	BrokerMosquitto BrokerKind = "mosquitto"
	BrokerEMQX      BrokerKind = "emqx"
)

var validBrokerKinds = map[BrokerKind]bool{
	BrokerMosquitto: true,
	BrokerEMQX:      true,
}

// ThroughputTier maps to batch size, flush interval, and replica count.
type ThroughputTier string

const (
	TierLow  ThroughputTier = "low"
	TierMid  ThroughputTier = "mid"
	TierHigh ThroughputTier = "high"
)

var validTiers = map[ThroughputTier]bool{
	TierLow: true, TierMid: true, TierHigh: true,
}

// IngestDirection controls whether the site ingest container exposes a downlink.
type IngestDirection string

const (
	DirectionUni IngestDirection = "uni"
	DirectionBi  IngestDirection = "bi"
)

var validDirections = map[IngestDirection]bool{
	DirectionUni: true, DirectionBi: true,
}

// PayloadCodec selects the ingest decode path.
type PayloadCodec string

const (
	CodecCBOR           PayloadCodec = "cbor"
	CodecJSON           PayloadCodec = "json"
	CodecRawPassthrough PayloadCodec = "raw_passthrough"
)

var validCodecs = map[PayloadCodec]bool{
	CodecCBOR: true, CodecJSON: true, CodecRawPassthrough: true,
}

// BrokerConfig holds per-site broker configuration.
type BrokerConfig struct {
	Kind    BrokerKind `yaml:"kind"`
	// SANAliases are additional DNS SANs to add to the site's server cert beyond
	// the default ste_<id>.tnt_<id>.<base-domain>.
	SANAliases []string `yaml:"san_aliases,omitempty"`
}

// IngestConfig holds per-site ingest configuration.
type IngestConfig struct {
	Direction IngestDirection `yaml:"direction"`
}

// siteIDPattern matches stored ste_-prefixed IDs.
var siteIDPattern = regexp.MustCompile(`^ste_[a-z][a-z0-9-]{0,40}$`)

// Site mirrors the on-disk site.yaml structure.
type Site struct {
	SchemaVersion int            `yaml:"schema_version"`
	ID            string         `yaml:"id"`        // ste_<slug>
	TenantID      string         `yaml:"tenant_id"` // tnt_<slug>
	Broker        BrokerConfig   `yaml:"broker"`
	Ingest        IngestConfig   `yaml:"ingest"`
	Throughput    ThroughputTier `yaml:"throughput"`
	PayloadCodec  PayloadCodec   `yaml:"payload_codec"`
	LeafTTLDays   int            `yaml:"leaf_ttl_days,omitempty"` // default 365
}

// Validate checks the Site for consistency and capability-matrix compliance.
func (s *Site) Validate() error {
	if s.SchemaVersion < 1 || s.SchemaVersion > version.MaxSupportedYAMLSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (binary supports up to %d)",
			s.SchemaVersion, version.MaxSupportedYAMLSchemaVersion)
	}
	if !siteIDPattern.MatchString(s.ID) {
		return fmt.Errorf("invalid site ID %q: must match ste_[a-z][a-z0-9-]{0,40}", s.ID)
	}
	if !tenantIDPattern.MatchString(s.TenantID) {
		return fmt.Errorf("invalid tenant_id %q: must match tnt_[a-z][a-z0-9-]{0,40}", s.TenantID)
	}
	if !validBrokerKinds[s.Broker.Kind] {
		return fmt.Errorf("invalid broker.kind %q: must be mosquitto or emqx", s.Broker.Kind)
	}
	if !validTiers[s.Throughput] {
		return fmt.Errorf("invalid throughput %q: must be low, mid, or high", s.Throughput)
	}
	if !validDirections[s.Ingest.Direction] {
		return fmt.Errorf("invalid ingest.direction %q: must be uni or bi", s.Ingest.Direction)
	}
	if !validCodecs[s.PayloadCodec] {
		return fmt.Errorf("invalid payload_codec %q: must be cbor, json, or raw_passthrough", s.PayloadCodec)
	}
	// Capability-matrix: mosquitto does not support MQTT5 shared subscriptions,
	// so mid-tier (2 replicas) + mosquitto is invalid.
	if s.Broker.Kind == BrokerMosquitto && s.Throughput == TierMid {
		return fmt.Errorf("invalid combination: broker.kind=mosquitto with throughput=mid requires 2 ingest replicas, "+
			"but mosquitto 2.x does not implement MQTT5 shared subscriptions; "+
			"use broker.kind=emqx or throughput=low")
	}
	// High tier is reserved for t3.large+; rejected at validation.
	if s.Throughput == TierHigh {
		return fmt.Errorf("throughput=high is not permitted in MVP: requires t3.large or larger; " +
			"use low or mid")
	}
	if s.LeafTTLDays == 0 {
		s.LeafTTLDays = 365
	}
	return nil
}

// IngestReplicas returns the number of ingest container replicas for this site.
func (s *Site) IngestReplicas() int {
	if s.Broker.Kind == BrokerEMQX && s.Throughput == TierMid {
		return 2
	}
	return 1
}

// BatchSize returns the configured batch size for the ingest ring buffer.
func (s *Site) BatchSize() int {
	switch s.Throughput {
	case TierMid:
		return 1000
	default: // low
		return 200
	}
}

// FlushIntervalMS returns the flush interval in milliseconds.
func (s *Site) FlushIntervalMS() int {
	switch s.Throughput {
	case TierMid:
		return 500
	default: // low
		return 1000
	}
}

// UseSharedSubscription returns true when the site uses EMQX shared
// subscriptions (EMQX broker with mid throughput = 2 replicas).
func (s *Site) UseSharedSubscription() bool {
	return s.Broker.Kind == BrokerEMQX && s.IngestReplicas() > 1
}

// MQTTTopicFilter returns the MQTT topic filter for this site's ingest.
// For shared subscriptions it returns the $share/... prefix form.
func (s *Site) MQTTTopicFilter() string {
	base := s.TenantID + "/" + s.ID + "/+/+"
	if s.UseSharedSubscription() {
		return "$share/" + s.ID + "/" + base
	}
	return base
}
