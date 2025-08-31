package client_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mokto/terraform-provider-hrobot/internal/client"
)

func newMockServer(t *testing.T) (*httptest.Server, *client.Client) {
	t.Helper()

	mux := http.NewServeMux()

	// POST /order/server/transaction
	mux.HandleFunc("/order/server/transaction", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.Form.Get("product_id") == "" {
			http.Error(w, `{"error":{"status":400,"code":"bad_request","message":"product_id required"}}`, 400)
			return
		}
		resp := map[string]any{
			"transaction": map[string]any{
				"id":     "txn-123",
				"status": "in process",
			},
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(resp)
	})

	// GET /order/server/transaction/txn-123
	mux.HandleFunc("/order/server/transaction/txn-123", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"transaction": map[string]any{
				"id":            "txn-123",
				"status":        "ready",
				"server_number": 424242,
				"server_ip":     "192.0.2.10",
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// POST /boot/424242/rescue
	mux.HandleFunc("/boot/424242/rescue", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("os") == "" {
			http.Error(w, `{"error":{"status":400,"code":"bad_request","message":"os required"}}`, 400)
			return
		}
		resp := map[string]any{
			"rescue": map[string]any{
				"server_ip": "192.0.2.10",
				"active":    true,
				"password":  "secret",
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// POST /reset/424242
	mux.HandleFunc("/reset/424242", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("type") == "" {
			http.Error(w, `{"error":{"status":400,"code":"bad_request","message":"type required"}}`, 400)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})

	ts := httptest.NewServer(mux)

	base, _ := url.Parse(ts.URL)
	cl := client.New(base.String(), "user", "pass", &http.Client{Timeout: 5 * time.Second})
	return ts, cl
}

func TestOrderServerAndGetTransaction(t *testing.T) {
	ts, cl := newMockServer(t)
	defer ts.Close()

	tx, err := cl.OrderServer(client.OrderParams{ProductID: "EX101", Test: true})
	if err != nil {
		t.Fatalf("OrderServer error: %v", err)
	}
	if tx.ID != "txn-123" {
		t.Fatalf("unexpected txn id: %s", tx.ID)
	}

	tx2, err := cl.GetOrderTransaction(tx.ID)
	if err != nil {
		t.Fatalf("GetOrderTransaction error: %v", err)
	}
	if tx2.Status != "ready" || tx2.ServerIP != "192.0.2.10" {
		t.Fatalf("unexpected txn: %+v", tx2)
	}
}

func TestActivateRescueAndReset(t *testing.T) {
	ts, cl := newMockServer(t)
	defer ts.Close()

	res, err := cl.ActivateRescue(424242, client.RescueParams{OS: "linux"})
	if err != nil {
		t.Fatalf("ActivateRescue error: %v", err)
	}
	if !res.Active || res.ServerIP != "192.0.2.10" {
		t.Fatalf("unexpected rescue: %+v", res)
	}
	if err := cl.Reset(424242, "hw"); err != nil {
		t.Fatalf("Reset error: %v", err)
	}
}
