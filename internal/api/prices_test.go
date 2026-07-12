package api

import (
	"errors"
	"testing"

	"autoapi/internal/model"
)

type priceStoreStub struct {
	StoreService
	prices []model.Price
	err    error
}

func (s *priceStoreStub) ListPrices() ([]model.Price, error) { return s.prices, s.err }

func (s *priceStoreStub) UpsertPrice(in model.PriceInput) (*model.Price, error) {
	if s.err != nil {
		return nil, s.err
	}
	p := &model.Price{ID: "price-1", UpstreamModel: in.UpstreamModel}
	s.prices = []model.Price{*p}
	return p, nil
}

func (s *priceStoreStub) DeletePrice(string) error { return s.err }

func TestPriceAPIMethodsPropagateStoreErrors(t *testing.T) {
	want := errors.New("price store unavailable")
	app := NewApp(Deps{Store: &priceStoreStub{err: want}})

	if _, err := app.ListPrices(); !errors.Is(err, want) {
		t.Fatalf("ListPrices error = %v, want %v", err, want)
	}
	if _, err := app.UpsertPrice(model.PriceInput{UpstreamModel: "m"}); !errors.Is(err, want) {
		t.Fatalf("UpsertPrice error = %v, want %v", err, want)
	}
	if err := app.DeletePrice("price-1"); !errors.Is(err, want) {
		t.Fatalf("DeletePrice error = %v, want %v", err, want)
	}
}

func TestPriceAPIMethodsReturnNotImplementedWithoutStore(t *testing.T) {
	app := NewApp(Deps{})
	if _, err := app.ListPrices(); !errors.Is(err, errNotImpl) {
		t.Fatalf("ListPrices error = %v, want errNotImpl", err)
	}
	if _, err := app.UpsertPrice(model.PriceInput{UpstreamModel: "m"}); !errors.Is(err, errNotImpl) {
		t.Fatalf("UpsertPrice error = %v, want errNotImpl", err)
	}
	if err := app.DeletePrice("price-1"); !errors.Is(err, errNotImpl) {
		t.Fatalf("DeletePrice error = %v, want errNotImpl", err)
	}
}
