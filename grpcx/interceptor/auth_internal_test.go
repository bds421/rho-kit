package interceptor

import "testing"

func TestWithAllowedSANsClonesInput(t *testing.T) {
	sans := []string{"svc-a.internal", "spiffe://example.org/svc-a"}
	opt := WithAllowedSANs(sans...)
	sans[0] = "mutated.internal"
	sans[1] = "spiffe://example.org/mutated"

	var cfg mtlsIdentityConfig
	opt(&cfg)

	if _, ok := cfg.allowedSANDNS["svc-a.internal"]; !ok {
		t.Fatalf("allowed DNS SANs = %v, want svc-a.internal", cfg.allowedSANDNS)
	}
	if _, ok := cfg.allowedSANDNS["mutated.internal"]; ok {
		t.Fatalf("allowed DNS SANs retained mutated input: %v", cfg.allowedSANDNS)
	}
	if _, ok := cfg.allowedSANURIs["spiffe://example.org/svc-a"]; !ok {
		t.Fatalf("allowed URI SANs = %v, want spiffe://example.org/svc-a", cfg.allowedSANURIs)
	}
	if _, ok := cfg.allowedSANURIs["spiffe://example.org/mutated"]; ok {
		t.Fatalf("allowed URI SANs retained mutated input: %v", cfg.allowedSANURIs)
	}
}

func TestWithAllowedCNsClonesInput(t *testing.T) {
	cns := []string{"svc-a"}
	opt := WithAllowedCNs(cns...)
	cns[0] = "mutated"

	var cfg mtlsIdentityConfig
	opt(&cfg)

	if _, ok := cfg.allowedCNs["svc-a"]; !ok {
		t.Fatalf("allowed CNs = %v, want svc-a", cfg.allowedCNs)
	}
	if _, ok := cfg.allowedCNs["mutated"]; ok {
		t.Fatalf("allowed CNs retained mutated input: %v", cfg.allowedCNs)
	}
}
