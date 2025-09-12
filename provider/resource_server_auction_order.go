package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/mokto/terraform-provider-hrobot/internal/client"
)

type serverAuctionOrderResource struct {
	providerData *ProviderData
}

type serverAuctionOrderModel struct {
	ID        types.String `tfsdk:"id"`
	ProductID types.Int64  `tfsdk:"product_id"`
	Dist      types.String `tfsdk:"dist"`
	Keys      types.List   `tfsdk:"authorized_key_fingerprints"`
	Password  types.String `tfsdk:"password"`
	Addons    types.List   `tfsdk:"addons"`
	Test      types.Bool   `tfsdk:"test"`

	TransactionID types.String `tfsdk:"transaction_id"`
	Status        types.String `tfsdk:"status"`
	ServerNumber  types.Int64  `tfsdk:"server_number"`
	ServerIP      types.String `tfsdk:"server_ip"`
}

// Cache entry for market transaction data
type marketTransactionCacheEntry struct {
	transaction *client.Transaction
	lastUpdated time.Time
}

// JSON-serializable cache entry for market transactions
type jsonMarketCacheEntry struct {
	Transaction *client.Transaction `json:"transaction"`
	LastUpdated string              `json:"last_updated"`
}

// Global cache for market transaction data to avoid hitting API rate limits
var (
	marketTransactionCache = make(map[string]*marketTransactionCacheEntry)
	marketCacheMutex       sync.RWMutex
	marketCacheExpiry      = 5 * time.Minute // Cache expires after 5 minutes
	marketCacheFile        = getMarketCacheFilePath()
)

// getMarketCacheFilePath returns the path to the market cache file in the .cache directory
func getMarketCacheFilePath() string {
	// Get the current working directory (should be the repository root)
	wd, err := os.Getwd()
	if err != nil {
		// Fallback to temp directory if we can't get working directory
		return filepath.Join(os.TempDir(), "terraform-provider-hrobot-market-cache.json")
	}

	// Create .cache directory if it doesn't exist
	cacheDir := filepath.Join(wd, ".cache")
	os.MkdirAll(cacheDir, 0755)

	return filepath.Join(cacheDir, "market-transaction-cache.json")
}

func NewResourceServerAuctionOrder() resource.Resource {
	// Load cache from disk on startup
	loadMarketCacheFromDisk()
	return &serverAuctionOrderResource{}
}

// loadMarketCacheFromDisk loads the market cache from disk
func loadMarketCacheFromDisk() {
	marketCacheMutex.Lock()
	defer marketCacheMutex.Unlock()

	data, err := os.ReadFile(marketCacheFile)
	if err != nil {
		// Cache file doesn't exist or can't be read, start with empty cache
		return
	}

	var diskCache map[string]*jsonMarketCacheEntry
	if err := json.Unmarshal(data, &diskCache); err != nil {
		// Invalid cache file, start with empty cache
		return
	}

	// Only load non-expired entries
	now := time.Now()
	for id, jsonEntry := range diskCache {
		lastUpdated, err := time.Parse(time.RFC3339, jsonEntry.LastUpdated)
		if err != nil {
			continue // Skip invalid timestamp
		}

		if now.Sub(lastUpdated) <= marketCacheExpiry {
			marketTransactionCache[id] = &marketTransactionCacheEntry{
				transaction: jsonEntry.Transaction,
				lastUpdated: lastUpdated,
			}
		}
	}
}

// saveMarketCacheToDisk saves the market cache to disk
func saveMarketCacheToDisk() {
	marketCacheMutex.RLock()
	defer marketCacheMutex.RUnlock()

	// Convert to JSON-serializable format
	jsonCache := make(map[string]*jsonMarketCacheEntry)
	for id, entry := range marketTransactionCache {
		jsonCache[id] = &jsonMarketCacheEntry{
			Transaction: entry.transaction,
			LastUpdated: entry.lastUpdated.Format(time.RFC3339),
		}
	}

	data, err := json.Marshal(jsonCache)
	if err != nil {
		return
	}

	os.WriteFile(marketCacheFile, data, 0600)
}

// getCachedMarketTransaction retrieves market transaction from cache if available and not expired
func getCachedMarketTransaction(id string) (*client.Transaction, bool) {
	marketCacheMutex.RLock()
	defer marketCacheMutex.RUnlock()

	entry, exists := marketTransactionCache[id]
	if !exists {
		return nil, false
	}

	// Check if cache entry is expired
	if time.Since(entry.lastUpdated) > marketCacheExpiry {
		return nil, false
	}

	return entry.transaction, true
}

// setCachedMarketTransaction stores market transaction in cache
func setCachedMarketTransaction(id string, transaction *client.Transaction) {
	marketCacheMutex.Lock()
	defer marketCacheMutex.Unlock()

	marketTransactionCache[id] = &marketTransactionCacheEntry{
		transaction: transaction,
		lastUpdated: time.Now(),
	}

	// Save to disk asynchronously
	go saveMarketCacheToDisk()
}

// shouldRefreshMarketTransaction determines if we need to refresh the market transaction data
func shouldRefreshMarketTransaction(transaction *client.Transaction) bool {
	if transaction == nil {
		return true
	}
	// Only refresh if status is "in process" - other statuses are final
	return transaction.Status == "in process"
}

func (r *serverAuctionOrderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_auction_order"
}

func (r *serverAuctionOrderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		Description: "Manages a Hetzner Robot server auction order. Orders servers from the auction/market at discounted prices. When destroyed, the server will be scheduled for cancellation at the end of the billing period.",
		Attributes: map[string]rschema.Attribute{
			"product_id": rschema.Int64Attribute{Required: true, Description: "Auction product id (e.g., 12345)"},
			"dist":       rschema.StringAttribute{Optional: true, Description: "Preinstall distribution label"},
			"authorized_key_fingerprints": rschema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Authorized key fingerprints stored in Robot",
			},
			"password": rschema.StringAttribute{
				Optional: true, Sensitive: true,
				Description: "Root password alternative to keys",
			},
			"addons": rschema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Addon ids (e.g., primary_ipv4)",
			},
			"test": rschema.BoolAttribute{Optional: true, Description: "Dry-run order"},

			"transaction_id": rschema.StringAttribute{Computed: true},
			"status":         rschema.StringAttribute{Computed: true},
			"server_number":  rschema.Int64Attribute{Computed: true},
			"server_ip":      rschema.StringAttribute{Computed: true, Description: "The server's IP address (available when server is ready)"},
			"id":             rschema.StringAttribute{Computed: true},
		},
	}
}

func (r *serverAuctionOrderResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.providerData = req.ProviderData.(*ProviderData)
}

func (r *serverAuctionOrderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serverAuctionOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keys := mustStringSliceCreateAuction(ctx, resp, plan.Keys)
	addons := mustStringSliceCreateAuction(ctx, resp, plan.Addons)
	if resp.Diagnostics.HasError() {
		return
	}

	tx, err := r.providerData.Client.OrderMarketServer(client.MarketOrderParams{
		ProductID: int(plan.ProductID.ValueInt64()),
		Keys:      keys,
		Addons:    addons,
		Test:      !plan.Test.IsNull() && plan.Test.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("auction order failed", err.Error())
		return
	}

	state := plan
	state.ID = types.StringValue(tx.ID)
	state.TransactionID = types.StringValue(tx.ID)
	state.Status = types.StringValue(tx.Status)
	if tx.ServerNumber != nil {
		state.ServerNumber = types.Int64Value(int64(*tx.ServerNumber))
	} else {
		state.ServerNumber = types.Int64Null()
	}
	state.ServerIP = types.StringValue(tx.ServerIP)

	// Cache the transaction data
	setCachedMarketTransaction(tx.ID, tx)

	tflog.Info(ctx, "created auction order", map[string]interface{}{"transaction_id": tx.ID})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverAuctionOrderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serverAuctionOrderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	transactionID := state.ID.ValueString()

	// Try to get cached transaction first
	cachedTx, found := getCachedMarketTransaction(transactionID)

	var tx *client.Transaction
	var err error

	// Determine if we need to refresh the data
	if found && !shouldRefreshMarketTransaction(cachedTx) {
		// Use cached data - transaction is in final state
		tx = cachedTx
		tflog.Info(ctx, "Using cached market transaction data", map[string]interface{}{
			"transaction_id": transactionID,
			"status":         tx.Status,
		})
	} else {
		// Make API call to get fresh data
		if found {
			tflog.Info(ctx, "Refreshing market transaction data (status is in process)", map[string]interface{}{
				"transaction_id": transactionID,
				"cached_status":  cachedTx.Status,
			})
		} else {
			tflog.Info(ctx, "No cached data found, fetching market transaction", map[string]interface{}{
				"transaction_id": transactionID,
			})
		}

		tx, err = r.providerData.Client.GetMarketOrderTransaction(transactionID)
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("read market transaction", err.Error())
			return
		}

		// Update cache with fresh data
		setCachedMarketTransaction(transactionID, tx)
		tflog.Info(ctx, "Updated market transaction cache", map[string]interface{}{
			"transaction_id": transactionID,
			"status":         tx.Status,
		})
	}

	state.Status = types.StringValue(tx.Status)
	if tx.ServerNumber != nil {
		state.ServerNumber = types.Int64Value(int64(*tx.ServerNumber))
	} else {
		state.ServerNumber = types.Int64Null()
	}
	state.ServerIP = types.StringValue(tx.ServerIP)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverAuctionOrderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// immutable; re-create on changes
	var plan serverAuctionOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.AddAttributeError(
		path.Root("product_id"),
		"Update Not Supported",
		"Auction order is immutable; destroy and re-create if needed.",
	)
}

func (r *serverAuctionOrderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Server auction order deletion is handled by the configuration resource
	// This resource only manages the order transaction, not server lifecycle
	tflog.Info(ctx, "server auction order resource deleted from state")
}

// helpers for auction orders
func mustStringSliceCreateAuction(ctx context.Context, resp *resource.CreateResponse, l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	resp.Diagnostics.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}

func optStringAuction(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}
