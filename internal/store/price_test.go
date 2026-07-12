package store

import (
	"math"
	"testing"
	"time"

	"autoapi/internal/model"
)

func TestPricesMigration(t *testing.T) {
	s := newTestStore(t)
	var name string
	if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='prices'`).Scan(&name); err != nil {
		t.Fatalf("prices table not found: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('prices') WHERE name='endpoint_kind'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("endpoint_kind column missing: err=%v n=%d", err, n)
	}
}

func TestPricesUpsertAndResolve(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "P", BaseURL: "https://p.example.com"})
	other, _ := s.CreateProvider(model.ProviderInput{Name: "O", BaseURL: "https://o.example.com"})

	global, err := s.UpsertPrice(model.PriceInput{
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD", Source: "global", Version: "v1", Confidence: model.CostConfidenceExact,
	})
	if err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	if global.ProviderID != "" {
		t.Fatalf("expected global provider id empty, got %q", global.ProviderID)
	}

	providerDefault, err := s.UpsertPrice(model.PriceInput{
		ProviderID:            p.ID,
		UpstreamModel:         "m",
		BillingMode:           model.BillingModeToken,
		InputPricePerMillion:  10,
		OutputPricePerMillion: 20,
		Currency:              "USD",
		Source:                "provider-default",
		Version:               "v2",
		Confidence:            model.CostConfidenceExact,
	})
	if err != nil {
		t.Fatalf("upsert provider default: %v", err)
	}
	_ = providerDefault

	providerEndpoint, err := s.UpsertPrice(model.PriceInput{
		ProviderID:            p.ID,
		UpstreamModel:         "m",
		EndpointKind:          "chat",
		BillingMode:           model.BillingModeToken,
		InputPricePerMillion:  100,
		OutputPricePerMillion: 200,
		Currency:              "USD",
		Source:                "provider-endpoint",
		Version:               "v3",
		Confidence:            model.CostConfidenceExact,
	})
	if err != nil {
		t.Fatalf("upsert provider endpoint: %v", err)
	}
	_ = providerEndpoint

	got, err := s.ResolvePrice(p.ID, "m", "chat")
	if err != nil {
		t.Fatalf("resolve endpoint: %v", err)
	}
	if got.Source != "provider-endpoint" {
		t.Fatalf("expected provider-endpoint, got %s", got.Source)
	}

	got, err = s.ResolvePrice(p.ID, "m", "embedding")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if got.Source != "provider-default" {
		t.Fatalf("expected provider-default, got %s", got.Source)
	}

	got, err = s.ResolvePrice(other.ID, "m", "chat")
	if err != nil {
		t.Fatalf("resolve other: %v", err)
	}
	if got.Source != "global" {
		t.Fatalf("expected global, got %s", got.Source)
	}

	got, err = s.ResolvePrice("", "m", "chat")
	if err != nil {
		t.Fatalf("resolve empty provider: %v", err)
	}
	if got.Source != "global" {
		t.Fatalf("expected global for empty provider, got %s", got.Source)
	}
}

func TestPricesResolveHardcodedAndUnknown(t *testing.T) {
	s := newTestStore(t)
	got, err := s.ResolvePrice("", "gpt-4o", "chat")
	if err != nil {
		t.Fatalf("resolve hardcoded: %v", err)
	}
	if got == nil {
		t.Fatal("expected hardcoded price for gpt-4o")
	}
	if got.Source != "hardcoded" {
		t.Fatalf("expected hardcoded source, got %s", got.Source)
	}
	if got.BillingMode != model.BillingModeToken {
		t.Fatalf("expected token billing, got %s", got.BillingMode)
	}
	if got.InputPricePerMillion <= 0 {
		t.Fatalf("expected positive input price, got %v", got.InputPricePerMillion)
	}

	got, err = s.ResolvePrice("", "unknown-model-xyz", "chat")
	if err != nil {
		t.Fatalf("resolve unknown: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for unknown model, got %+v", got)
	}
}

func TestPricesUpsertReplacesGlobal(t *testing.T) {
	s := newTestStore(t)
	_, err := s.UpsertPrice(model.PriceInput{
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD", Version: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpsertPrice(model.PriceInput{
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 5, OutputPricePerMillion: 6,
		Currency: "USD", Version: "v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPrices()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 price after upsert, got %d", len(list))
	}
	if list[0].InputPricePerMillion != 5 || list[0].Version != "v2" {
		t.Fatalf("expected price updated to v2/5, got %+v", list[0])
	}
	if updated.ID != list[0].ID {
		t.Fatalf("expected ID preserved on upsert, got %s vs %s", updated.ID, list[0].ID)
	}
}

func TestPricesResolveInvalidAndExpiredFallsBack(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "P", BaseURL: "https://p.example.com"})
	if _, err := s.UpsertPrice(model.PriceInput{
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD", Source: "global",
	}); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	_, err := s.UpsertPrice(model.PriceInput{
		ProviderID:           p.ID,
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency:  "USD",
		ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(),
		Source:    "expired",
	})
	if err != nil {
		t.Fatalf("upsert expired: %v", err)
	}
	got, err := s.ResolvePriceAt(p.ID, "m", "chat", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("resolve expired: %v", err)
	}
	if got == nil || got.Source != "global" {
		t.Fatalf("expected global fallback, got %+v", got)
	}
}

func TestPricesResolveNotYetEffectiveFallsBack(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "P", BaseURL: "https://p.example.com"})
	if _, err := s.UpsertPrice(model.PriceInput{
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD", Source: "global",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertPrice(model.PriceInput{
		ProviderID:           p.ID,
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 10, OutputPricePerMillion: 20,
		Currency: "USD", Source: "future",
		EffectiveAt: 2000,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolvePriceAt(p.ID, "m", "chat", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Source != "global" {
		t.Fatalf("expected global fallback, got %+v", got)
	}
}

func TestPricesNormalizeProviderAndUSD(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "P", BaseURL: "https://p.example.com"})
	got, err := s.UpsertPrice(model.PriceInput{
		ProviderID:            "  " + p.ID + "  ",
		UpstreamModel:         "  m  ",
		EndpointKind:          "  chat  ",
		BillingMode:           model.BillingModeToken,
		InputPricePerMillion:  1,
		OutputPricePerMillion: 2,
		Currency:              " usd ",
		Source:                " source ",
		Version:               " v1 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderID != p.ID || got.UpstreamModel != "m" || got.EndpointKind != "chat" || got.Currency != "USD" || got.Source != "source" || got.Version != "v1" {
		t.Fatalf("normalization failed: %+v", got)
	}
}

func TestPricesRejectInvalidInput(t *testing.T) {
	s := newTestStore(t)
	cases := []model.PriceInput{
		{UpstreamModel: "m", InputPricePerMillion: -1},
		{UpstreamModel: "m", OutputPricePerMillion: math.NaN()},
		{UpstreamModel: "m", RequestPricePerRequest: math.Inf(1)},
		{UpstreamModel: "m", EffectiveAt: 2, ExpiresAt: 1},
		{UpstreamModel: "m", Currency: "EUR"},
	}
	for i, in := range cases {
		if _, err := s.UpsertPrice(in); err == nil {
			t.Fatalf("case %d: expected invalid price rejection", i)
		}
	}
}

func TestPricesSchemaConstraintsAndCascade(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO prices (id, upstream_model, input_price_per_million, updated_at, created_at) VALUES ('global-1', 'unique-model', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO prices (id, upstream_model, input_price_per_million, updated_at, created_at) VALUES ('global-2', 'unique-model', 1, 1, 1)`); err == nil {
		t.Fatal("expected duplicate global key rejection")
	}
	if _, err := s.db.Exec(`INSERT INTO prices (id, upstream_model, input_price_per_million, updated_at, created_at) VALUES ('negative', 'negative-model', -1, 1, 1)`); err == nil {
		t.Fatal("expected schema non-negative CHECK rejection")
	}
	if _, err := s.db.Exec(`INSERT INTO prices (id, upstream_model, effective_at, expires_at, updated_at, created_at) VALUES ('time-order', 'time-model', 2, 1, 1, 1)`); err == nil {
		t.Fatal("expected schema effective/expiry CHECK rejection")
	}

	p, _ := s.CreateProvider(model.ProviderInput{Name: "Cascade", BaseURL: "https://cascade.example.com"})
	if _, err := s.UpsertPrice(model.PriceInput{ProviderID: p.ID, UpstreamModel: "cascade-model", BillingMode: model.BillingModeToken, InputPricePerMillion: 1, OutputPricePerMillion: 1, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProvider(p.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prices WHERE upstream_model = 'cascade-model'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected provider price cascade delete, got %d rows", count)
	}
}

func TestPricesDelete(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "P", BaseURL: "https://p.example.com"})

	// Insert two prices (one global, one provider-scoped).
	if _, err := s.UpsertPrice(model.PriceInput{
		UpstreamModel:        "g",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD",
	}); err != nil {
		t.Fatal(err)
	}
	providerPrice, err := s.UpsertPrice(model.PriceInput{
		ProviderID:           p.ID,
		UpstreamModel:        "p",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 3, OutputPricePerMillion: 4,
		Currency: "USD",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Deleting a non-existent ID is a no-op (idempotent).
	if err := s.DeletePrice("does-not-exist"); err != nil {
		t.Fatalf("expected idempotent delete, got %v", err)
	}

	// Delete the provider-scoped record and verify only the global remains.
	if err := s.DeletePrice(providerPrice.ID); err != nil {
		t.Fatalf("delete provider price: %v", err)
	}
	list, err := s.ListPrices()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].UpstreamModel != "g" {
		t.Fatalf("expected only global to remain, got %+v", list)
	}

	// Re-upsert the same key after delete is a fresh insert.
	again, err := s.UpsertPrice(model.PriceInput{
		ProviderID:           p.ID,
		UpstreamModel:        "p",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 5, OutputPricePerMillion: 6,
		Currency: "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == providerPrice.ID {
		t.Fatalf("expected new ID after re-insert, got %s", again.ID)
	}
}

func TestPricesResolveAfterDelete(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UpsertPrice(model.PriceInput{
		UpstreamModel:        "only",
		BillingMode:          model.BillingModeToken,
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD",
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListPrices()
	if err := s.DeletePrice(list[0].ID); err != nil {
		t.Fatal(err)
	}
	// After delete, resolve should fall through to hardcoded / nil.
	got, err := s.ResolvePrice("", "only", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil for deleted model, got %+v", got)
	}
}
