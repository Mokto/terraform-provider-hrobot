package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	providerpkg "github.com/mokto/terraform-provider-hrobot/provider"
)

// ProtoV6 factories for Plugin Framework providers.
// NOTE: signature returns tfprotov6.ProviderServer.
func testProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"hrobot": providerserver.NewProtocol6WithError(providerpkg.New("test")()),
	}
}

// Minimal mock of Hetzner Robot endpoints used by tests.
func newRobotMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	// POST /order/server/transaction
	mux.HandleFunc("/order/server/transaction", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Form.Get("product_id") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"status": 400, "code": "bad_request", "message": "product_id required"},
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"transaction": map[string]any{
				"id":     "txn-acc",
				"status": "in process",
			},
		})
	})

	// GET /order/server/transaction/txn-acc
	mux.HandleFunc("/order/server/transaction/txn-acc", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"transaction": map[string]any{
				"id":            "txn-acc",
				"status":        "ready",
				"server_number": 111111,
				"server_ip":     "198.51.100.20",
			},
		})
	})

	// POST /boot/111111/rescue
	mux.HandleFunc("/boot/111111/rescue", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rescue": map[string]any{
				"server_ip": "198.51.100.20",
				"active":    true,
				"password":  "pw",
			},
		})
	})

	// POST /reset/111111
	mux.HandleFunc("/reset/111111", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})

	return httptest.NewServer(mux)
}

func TestAcc_ServerOrder_Basic(t *testing.T) {
	ts := newRobotMockServer(t)
	defer ts.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "hrobot" {
  username = "u"
  password = "p"
  base_url = "%s"
}

resource "hrobot_server_order" "ex101" {
  product_id = "EX101"
}
`, ts.URL),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("hrobot_server_order.ex101", "transaction_id", "txn-acc"),
					resource.TestCheckResourceAttr("hrobot_server_order.ex101", "status", "in process"),
				),
			},
		},
	})
}

func TestAcc_ServerOrder_CachingBehavior(t *testing.T) {
	// Create a mock server that tracks transaction API calls specifically
	transactionCallCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/order/server/transaction" {
			transactionCallCount++
			// Order creation - returns "in process" status
			_ = json.NewEncoder(w).Encode(map[string]any{
				"transaction": map[string]any{
					"id":     "txn-cache-test",
					"status": "in process",
				},
			})
		} else if r.URL.Path == "/order/server/transaction/txn-cache-test" {
			transactionCallCount++
			// Transaction lookup - return ready status (simulating status change)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"transaction": map[string]any{
					"id":            "txn-cache-test",
					"status":        "ready",
					"server_number": 123456,
					"server_ip":     "192.168.1.100",
					"date":          "2024-01-01T00:00:00Z",
				},
			})
		} else if r.Method == "DELETE" && r.URL.Path == "/server/123456/cancellation" {
			// Server cancellation - not counted as transaction call
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer ts.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				// First step - create order (should make 1 transaction API call: POST)
				Config: fmt.Sprintf(`
provider "hrobot" {
  username = "u"
  password = "p"
  base_url = "%s"
}

resource "hrobot_server_order" "test" {
  product_id = "EX101"
}
`, ts.URL),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("hrobot_server_order.test", "transaction_id", "txn-cache-test"),
					resource.TestCheckResourceAttr("hrobot_server_order.test", "status", "in process"),
				),
			},
			{
				// Second step - should make API call since status is "in process" and get updated status
				Config: fmt.Sprintf(`
provider "hrobot" {
  username = "u"
  password = "p"
  base_url = "%s"
}

resource "hrobot_server_order" "test" {
  product_id = "EX101"
}
`, ts.URL),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("hrobot_server_order.test", "transaction_id", "txn-cache-test"),
					resource.TestCheckResourceAttr("hrobot_server_order.test", "status", "ready"),
					resource.TestCheckResourceAttr("hrobot_server_order.test", "server_ip", "192.168.1.100"),
				),
			},
			{
				// Third step - should use cached data since status is now "ready" (final state)
				Config: fmt.Sprintf(`
provider "hrobot" {
  username = "u"
  password = "p"
  base_url = "%s"
}

resource "hrobot_server_order" "test" {
  product_id = "EX101"
}
`, ts.URL),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("hrobot_server_order.test", "transaction_id", "txn-cache-test"),
					resource.TestCheckResourceAttr("hrobot_server_order.test", "status", "ready"),
					resource.TestCheckResourceAttr("hrobot_server_order.test", "server_ip", "192.168.1.100"),
				),
			},
		},
	})

	// Verify that only 2 transaction API calls were made:
	// 1. POST for order creation
	// 2. GET for first read (status "in process" -> makes API call)
	// 3. GET for second read (status "ready" -> uses cached data, no API call)
	if transactionCallCount != 2 {
		t.Errorf("Expected 2 transaction API calls (POST + GET), got %d", transactionCallCount)
	}
}

// Test removed - data source no longer exists

// Data source caching test removed - data source no longer exists

// keep a reference so linters don't complain about unused imports in some setups
var _ = context.Background()
