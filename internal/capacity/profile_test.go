package capacity_test

import (
	"testing"

	"controlai/internal/capacity"
)

// TestProfileTableContainsAllValidPermutations verifies that every
// capability-matrix-allowed combination has a profile entry.
// This is the CI assertion described in tasks.md 10.5.
func TestProfileTableContainsAllValidPermutations(t *testing.T) {
	// Valid combinations per capability matrix (design D6):
	// mosquitto: low only (mid is rejected). EMQX: low and mid.
	valid := []capacity.Key{
		{BrokerKind: "mosquitto", Tier: "low", Direction: "uni"},
		{BrokerKind: "mosquitto", Tier: "low", Direction: "bi"},
		{BrokerKind: "emqx", Tier: "low", Direction: "uni"},
		{BrokerKind: "emqx", Tier: "low", Direction: "bi"},
		{BrokerKind: "emqx", Tier: "mid", Direction: "uni"},
		{BrokerKind: "emqx", Tier: "mid", Direction: "bi"},
	}
	for _, k := range valid {
		p, err := capacity.SiteProfile(k.BrokerKind, k.Tier, k.Direction)
		if err != nil {
			t.Errorf("missing profile for %+v: %v", k, err)
			continue
		}
		if p.BrokerMB <= 0 || p.IngestMB <= 0 {
			t.Errorf("profile %+v has zero or negative MB values: %+v", k, p)
		}
	}
	// Invalid combination: mosquitto+mid must not have a profile.
	_, err := capacity.SiteProfile("mosquitto", "mid", "uni")
	if err == nil {
		t.Error("expected no profile for mosquitto+mid+uni (capability-matrix-rejected)")
	}
}

func TestPredict_AdmissionAllowed(t *testing.T) {
	plan := []capacity.TenantPlan{
		{
			TenantID: "tnt_alpha",
			Sites: []capacity.SitePlan{
				{SiteID: "ste_s1", BrokerKind: "mosquitto", Tier: "low", Direction: "uni"},
			},
		},
	}
	// 4 GiB host = 4*1024*1024 kB
	pred, err := capacity.Predict(plan, 4*1024*1024)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if !pred.Admissible {
		t.Errorf("expected admissible for single mosquitto-low-uni site on 4 GiB host; got projected=%d max=%d",
			pred.ProjectedMB, pred.MaxAllowedMB)
	}
}

func TestPredict_HighLoadRejected(t *testing.T) {
	// Fill host with 10 EMQX-mid-bi sites to push over 85%.
	var tenants []capacity.TenantPlan
	for i := 0; i < 10; i++ {
		tp := capacity.TenantPlan{
			TenantID: "tnt_stress",
		}
		for j := 0; j < 10; j++ {
			tp.Sites = append(tp.Sites, capacity.SitePlan{
				SiteID:     "ste_x",
				BrokerKind: "emqx",
				Tier:       "mid",
				Direction:  "bi",
			})
		}
		tenants = append(tenants, tp)
	}
	pred, err := capacity.Predict(tenants, 4*1024*1024)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if pred.Admissible {
		t.Errorf("expected inadmissible for 100 emqx-mid-bi sites on 4 GiB host; projected=%d max=%d",
			pred.ProjectedMB, pred.MaxAllowedMB)
	}
}

func TestPredict_BiAddsRSS(t *testing.T) {
	uniPlan := []capacity.TenantPlan{{TenantID: "t", Sites: []capacity.SitePlan{
		{SiteID: "s", BrokerKind: "mosquitto", Tier: "low", Direction: "uni"},
	}}}
	biPlan := []capacity.TenantPlan{{TenantID: "t", Sites: []capacity.SitePlan{
		{SiteID: "s", BrokerKind: "mosquitto", Tier: "low", Direction: "bi"},
	}}}
	memKB := int64(4 * 1024 * 1024)
	uniPred, _ := capacity.Predict(uniPlan, memKB)
	biPred, _ := capacity.Predict(biPlan, memKB)
	if biPred.ProjectedMB <= uniPred.ProjectedMB {
		t.Errorf("bi should use more RSS than uni: bi=%d, uni=%d", biPred.ProjectedMB, uniPred.ProjectedMB)
	}
}
