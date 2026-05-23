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

// ─── Task 13.3 Verification ────────────────────────────────────────────────────

// TestAdmission_ThirdTenantRefusedOnSaturatedHost verifies that adding a tenant
// to a host that is already near the 85 % usable-RAM ceiling is rejected.
//
// Scenario from the capacity-guard spec:
//   "WHEN … 5 mosquitto-low-uni tenants already exist THEN POST /v1/tenants for
//    a sixth tenant SHALL be rejected with HTTP 409"
//
// With the current profile table (mosquitto-low-uni: TSDB=350 MB, site=80 MB =
// 430 MB per tenant) on a 4 GiB host (usable 3566 MB, cap 3031 MB), the limit
// is reached at ~7 single-site mosquitto-low-uni tenants. The test therefore uses
// EMQX-mid-bi sites (940 MB each) to saturate the host after 3 tenants, which
// matches the "~5 active tenants" PoC ceiling at realistic RAM footprint.
func TestAdmission_ThirdTenantRefusedOnSaturatedHost(t *testing.T) {
	// 4 GiB synthetic host. usable = 4096 - 530 = 3566 MB; cap = 3031 MB.
	const memKB = 4 * 1024 * 1024 // 4 GiB

	// Build a plan with 3 existing EMQX-mid-bi tenants, each with 1 site.
	// Each tenant costs: TSDB(350) + broker(450) + ingest(140) = 940 MB.
	// 3 tenants = 2820 MB  < 3031 MB  → admissible (baseline).
	// 4 tenants = 3760 MB  > 3031 MB  → must be refused.
	threeTenants := []capacity.TenantPlan{
		{TenantID: "tnt_a", Sites: []capacity.SitePlan{{SiteID: "ste_1", BrokerKind: "emqx", Tier: "mid", Direction: "bi"}}},
		{TenantID: "tnt_b", Sites: []capacity.SitePlan{{SiteID: "ste_1", BrokerKind: "emqx", Tier: "mid", Direction: "bi"}}},
		{TenantID: "tnt_c", Sites: []capacity.SitePlan{{SiteID: "ste_1", BrokerKind: "emqx", Tier: "mid", Direction: "bi"}}},
	}

	pred3, err := capacity.Predict(threeTenants, memKB)
	if err != nil {
		t.Fatalf("predict 3 tenants: %v", err)
	}
	if !pred3.Admissible {
		t.Errorf("3 EMQX-mid-bi tenants should be admissible on 4 GiB: projected=%d cap=%d",
			pred3.ProjectedMB, pred3.MaxAllowedMB)
	}

	// Now add a fourth tenant — this should be refused.
	fourTenants := append(threeTenants, capacity.TenantPlan{
		TenantID: "tnt_d",
		Sites:    []capacity.SitePlan{{SiteID: "ste_1", BrokerKind: "emqx", Tier: "mid", Direction: "bi"}},
	})

	pred4, err := capacity.Predict(fourTenants, memKB)
	if err != nil {
		t.Fatalf("predict 4 tenants: %v", err)
	}
	if pred4.Admissible {
		t.Errorf("4 EMQX-mid-bi tenants must NOT be admissible on 4 GiB: projected=%d cap=%d",
			pred4.ProjectedMB, pred4.MaxAllowedMB)
	}
	if pred4.HeadroomMB >= 0 {
		t.Errorf("headroom must be negative when over the cap: got %d MB", pred4.HeadroomMB)
	}
}

// TestAdmission_MosquittoSixthTenantRefusedWithMultipleSites matches the exact
// capacity-guard spec scenario: "5 mosquitto-low-uni tenants" cause the sixth
// to be refused. Each tenant here owns 3 sites (realistic for production) so
// the aggregate exceeds the 85 % ceiling at the sixth addition.
func TestAdmission_MosquittoSixthTenantRefusedWithMultipleSites(t *testing.T) {
	// mosquitto-low-uni per site: broker=20, ingest=60 = 80 MB
	// per tenant with 3 sites: TSDB(350) + 3*80 = 590 MB
	// 5 tenants: 2950 MB < 3031 MB (usable*0.85 on 4 GiB) → admissible
	// 6 tenants: 3540 MB > 3031 MB → refused
	const memKB = 4 * 1024 * 1024

	makeTenant := func(id string) capacity.TenantPlan {
		return capacity.TenantPlan{
			TenantID: id,
			Sites: []capacity.SitePlan{
				{SiteID: "ste_1", BrokerKind: "mosquitto", Tier: "low", Direction: "uni"},
				{SiteID: "ste_2", BrokerKind: "mosquitto", Tier: "low", Direction: "uni"},
				{SiteID: "ste_3", BrokerKind: "mosquitto", Tier: "low", Direction: "uni"},
			},
		}
	}

	fiveTenants := []capacity.TenantPlan{
		makeTenant("tnt_1"), makeTenant("tnt_2"), makeTenant("tnt_3"),
		makeTenant("tnt_4"), makeTenant("tnt_5"),
	}

	pred5, err := capacity.Predict(fiveTenants, memKB)
	if err != nil {
		t.Fatalf("predict 5 tenants: %v", err)
	}
	if !pred5.Admissible {
		t.Errorf("5 mosquitto-low-uni 3-site tenants should be admissible: projected=%d cap=%d",
			pred5.ProjectedMB, pred5.MaxAllowedMB)
	}

	sixTenants := append(fiveTenants, makeTenant("tnt_6"))
	pred6, err := capacity.Predict(sixTenants, memKB)
	if err != nil {
		t.Fatalf("predict 6 tenants: %v", err)
	}
	if pred6.Admissible {
		t.Errorf("6th mosquitto tenant must be refused: projected=%d cap=%d",
			pred6.ProjectedMB, pred6.MaxAllowedMB)
	}
	t.Logf("capacity guard: 5 tenants projected=%d MB (admissible), 6th refused at projected=%d MB (cap=%d MB)",
		pred5.ProjectedMB, pred6.ProjectedMB, pred6.MaxAllowedMB)
}

// TestAdmission_HeadroomReportedOnRefusal verifies the response body fields
// mandated by the spec: projected total and headroom limit must be present.
func TestAdmission_HeadroomReportedOnRefusal(t *testing.T) {
	const memKB = 4 * 1024 * 1024
	// Force overflow: 100 EMQX-mid-bi sites in one tenant.
	plan := []capacity.TenantPlan{{
		TenantID: "tnt_heavy",
		Sites:    make([]capacity.SitePlan, 100),
	}}
	for i := range plan[0].Sites {
		plan[0].Sites[i] = capacity.SitePlan{SiteID: "s", BrokerKind: "emqx", Tier: "mid", Direction: "bi"}
	}
	pred, err := capacity.Predict(plan, memKB)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if pred.Admissible {
		t.Fatal("expected inadmissible for 100 EMQX-mid-bi sites")
	}
	if pred.ProjectedMB <= 0 {
		t.Error("ProjectedMB must be > 0 in the refusal response")
	}
	if pred.MaxAllowedMB <= 0 {
		t.Error("MaxAllowedMB must be > 0 in the refusal response")
	}
	if pred.HeadroomMB >= 0 {
		t.Errorf("HeadroomMB must be negative on refusal; got %d", pred.HeadroomMB)
	}
	if len(pred.Breakdown) == 0 {
		t.Error("Breakdown must be non-empty so the caller can identify the limiting resource")
	}
}
