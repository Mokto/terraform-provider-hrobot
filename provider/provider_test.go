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

func TestAcc_OrderTransaction_DataAndGuardedInstall(t *testing.T) {
	ts := newRobotMockServer(t)
	defer ts.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				// The data source returns server_number/ip; installimage is count=0 to avoid SSH in test.
				Config: fmt.Sprintf(`
provider "hrobot" {
  username = "u"
  password = "p"
  base_url = "%s"
}

resource "hrobot_server_order" "ex101" {
  product_id = "EX101"
}

data "hrobot_order_transaction" "ex101" {
  transaction_id = hrobot_server_order.ex101.transaction_id
}

resource "hrobot_installimage" "bootstrap" {
  count         = 0
  server_number = try(data.hrobot_order_transaction.ex101.server_number, 0)
  autosetup_content = <<-EOT
HOSTNAME test
DRIVE1 /dev/sda
SWRAID 0
IMAGE /images/Debian-1204-bookworm-64-minimal.tar.gz
POST_INSTALL /root/post-install.sh
EOT
}
`, ts.URL),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("hrobot_server_order.ex101", "transaction_id", "txn-acc"),
					resource.TestCheckResourceAttr("data.hrobot_order_transaction.ex101", "server_ip", "198.51.100.20"),
				),
			},
		},
	})
}

// keep a reference so linters don't complain about unused imports in some setups
var _ = context.Background()
