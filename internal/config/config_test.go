package config_test

import (
	"testing"

	"controlai/internal/config"
)

func TestValidateSlug(t *testing.T) {
	valid := []string{"acme-corp", "a", "foo123", "abc-def-ghi"}
	for _, s := range valid {
		if err := config.ValidateSlug(s); err != nil {
			t.Errorf("expected %q to be valid: %v", s, err)
		}
	}
	invalid := []string{"", "ACME", "1abc", "-abc", "abc-", "a b c", "acme_corp"}
	for _, s := range invalid {
		if err := config.ValidateSlug(s); err == nil {
			t.Errorf("expected %q to be invalid", s)
		}
	}
}

func TestTenantValidate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		tenant := &config.Tenant{
			SchemaVersion: 1,
			ID:            "tnt_acme-corp",
			Domain:        "example.com",
			Retention:     config.Retention7d,
		}
		if err := tenant.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("invalid retention", func(t *testing.T) {
		tenant := &config.Tenant{
			SchemaVersion: 1,
			ID:            "tnt_acme-corp",
			Domain:        "example.com",
			Retention:     "2d", // not allowed
		}
		if err := tenant.Validate(); err == nil {
			t.Fatal("expected error for invalid retention")
		}
	})
	t.Run("invalid schema_version", func(t *testing.T) {
		tenant := &config.Tenant{
			SchemaVersion: 99,
			ID:            "tnt_acme-corp",
			Domain:        "example.com",
			Retention:     config.Retention1d,
		}
		if err := tenant.Validate(); err == nil {
			t.Fatal("expected error for unsupported schema_version")
		}
	})
	t.Run("invalid ID", func(t *testing.T) {
		tenant := &config.Tenant{
			SchemaVersion: 1,
			ID:            "acme-corp", // missing tnt_ prefix
			Domain:        "example.com",
			Retention:     config.Retention1d,
		}
		if err := tenant.Validate(); err == nil {
			t.Fatal("expected error for invalid tenant ID")
		}
	})
}

func TestSiteValidate(t *testing.T) {
	t.Run("valid mosquitto low uni", func(t *testing.T) {
		site := &config.Site{
			SchemaVersion: 1,
			ID:            "ste_seoul",
			TenantID:      "tnt_acme-corp",
			Broker:        config.BrokerConfig{Kind: config.BrokerMosquitto},
			Ingest:        config.IngestConfig{Direction: config.DirectionUni},
			Throughput:    config.TierLow,
			PayloadCodec:  config.CodecCBOR,
		}
		if err := site.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("mosquitto + mid rejected (capability matrix)", func(t *testing.T) {
		site := &config.Site{
			SchemaVersion: 1,
			ID:            "ste_seoul",
			TenantID:      "tnt_acme-corp",
			Broker:        config.BrokerConfig{Kind: config.BrokerMosquitto},
			Ingest:        config.IngestConfig{Direction: config.DirectionUni},
			Throughput:    config.TierMid, // invalid: mosquitto + mid
			PayloadCodec:  config.CodecCBOR,
		}
		if err := site.Validate(); err == nil {
			t.Fatal("expected capability-matrix rejection for mosquitto+mid")
		}
	})
	t.Run("high tier rejected", func(t *testing.T) {
		site := &config.Site{
			SchemaVersion: 1,
			ID:            "ste_seoul",
			TenantID:      "tnt_acme-corp",
			Broker:        config.BrokerConfig{Kind: config.BrokerEMQX},
			Ingest:        config.IngestConfig{Direction: config.DirectionUni},
			Throughput:    config.TierHigh,
			PayloadCodec:  config.CodecCBOR,
		}
		if err := site.Validate(); err == nil {
			t.Fatal("expected error for high tier")
		}
	})
	t.Run("emqx mid bi valid", func(t *testing.T) {
		site := &config.Site{
			SchemaVersion: 1,
			ID:            "ste_busan",
			TenantID:      "tnt_acme-corp",
			Broker:        config.BrokerConfig{Kind: config.BrokerEMQX},
			Ingest:        config.IngestConfig{Direction: config.DirectionBi},
			Throughput:    config.TierMid,
			PayloadCodec:  config.CodecJSON,
		}
		if err := site.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSiteHelpers(t *testing.T) {
	siteEMQXMid := &config.Site{
		SchemaVersion: 1,
		ID:            "ste_seoul",
		TenantID:      "tnt_acme-corp",
		Broker:        config.BrokerConfig{Kind: config.BrokerEMQX},
		Ingest:        config.IngestConfig{Direction: config.DirectionUni},
		Throughput:    config.TierMid,
		PayloadCodec:  config.CodecCBOR,
	}
	if siteEMQXMid.IngestReplicas() != 2 {
		t.Errorf("expected 2 replicas for EMQX mid, got %d", siteEMQXMid.IngestReplicas())
	}
	if !siteEMQXMid.UseSharedSubscription() {
		t.Error("expected shared subscription for EMQX mid")
	}
	if siteEMQXMid.BatchSize() != 1000 {
		t.Errorf("expected batch size 1000 for mid, got %d", siteEMQXMid.BatchSize())
	}
	if siteEMQXMid.FlushIntervalMS() != 500 {
		t.Errorf("expected flush 500ms for mid, got %d", siteEMQXMid.FlushIntervalMS())
	}

	siteMoqLow := &config.Site{
		Broker:     config.BrokerConfig{Kind: config.BrokerMosquitto},
		Throughput: config.TierLow,
	}
	if siteMoqLow.IngestReplicas() != 1 {
		t.Errorf("expected 1 replica for mosquitto low, got %d", siteMoqLow.IngestReplicas())
	}
	if siteMoqLow.BatchSize() != 200 {
		t.Errorf("expected batch size 200 for low, got %d", siteMoqLow.BatchSize())
	}
}

func TestChunkIntervals(t *testing.T) {
	cases := []struct {
		r        config.Retention
		expected string
	}{
		{config.Retention1m, "1 minute"},
		{config.Retention1h, "1 minute"},
		{config.Retention1d, "15 minutes"},
		{config.Retention7d, "1 hour"},
		{config.Retention30d, "6 hours"},
	}
	for _, tc := range cases {
		t := t
		t.Run(string(tc.r), func(t *testing.T) {
			tenant := &config.Tenant{Retention: tc.r}
			got := tenant.ChunkTimeInterval()
			if got != tc.expected {
				t.Errorf("retention=%s: got chunk interval %q, want %q", tc.r, got, tc.expected)
			}
		})
	}
}

func TestCompressionEnabled(t *testing.T) {
	on := []config.Retention{config.Retention7d, config.Retention30d}
	for _, r := range on {
		tenant := &config.Tenant{Retention: r}
		if !tenant.CompressionEnabled() {
			t.Errorf("expected compression enabled for retention=%s", r)
		}
	}
	off := []config.Retention{config.Retention1m, config.Retention1h, config.Retention1d}
	for _, r := range off {
		tenant := &config.Tenant{Retention: r}
		if tenant.CompressionEnabled() {
			t.Errorf("expected compression disabled for retention=%s", r)
		}
	}
}
