package service

import (
	"testing"

	"autoapi/internal/model"
	"autoapi/internal/store"
)

func TestPriceManagementDelegatesToStore(t *testing.T) {
	st, err := store.New(t.Context(), store.StoreDeps{DSN: ":memory:"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	svc := New(st, nil, t.TempDir())
	p, err := svc.UpsertPrice(model.PriceInput{UpstreamModel: "delegated-model", BillingMode: model.BillingModeToken, InputPricePerMillion: 1.25, OutputPricePerMillion: 2.5, Currency: "USD"})
	if err != nil {
		t.Fatalf("UpsertPrice: %v", err)
	}
	prices, err := svc.ListPrices()
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	if len(prices) != 1 || prices[0].ID != p.ID {
		t.Fatalf("unexpected prices: %+v", prices)
	}
	if err := svc.DeletePrice(p.ID); err != nil {
		t.Fatalf("DeletePrice: %v", err)
	}
	prices, err = svc.ListPrices()
	if err != nil {
		t.Fatalf("ListPrices after delete: %v", err)
	}
	if len(prices) != 0 {
		t.Fatalf("expected no prices after delete, got %+v", prices)
	}
}
