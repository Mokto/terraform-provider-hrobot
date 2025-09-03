# Bulk Data Fetching with Smart Caching

This provider implements **bulk data fetching with smart caching** to solve the Hetzner Robot API rate limiting issue (200 calls per hour).

## The Problem

- **Individual API calls**: `/server/321` = 1 call per server
- **With 10 servers**: 10 API calls per refresh
- **With 100 servers**: 100 API calls per refresh
- **Rate limit**: 200 calls per hour = **2 hours max** for 100 servers

## The Solution

- **Bulk API call**: `/server` = 1 call for ALL servers
- **Smart caching**: Fetches once per Terraform apply
- **With 100 servers**: 1 API call per apply = **200 applies per hour**

## How It Works

### 1. Cache Manager
```go
type CacheManager struct {
    servers   []Server
    fetched   bool
    mutex     sync.RWMutex
}
```

- **Thread-safe**: Multiple resources can access simultaneously
- **One-time fetch**: Calls `/server` endpoint only once per apply
- **Memory efficient**: Stores data in memory for the duration of the apply

### 2. Provider Integration
```go
type ProviderData struct {
    Client       *client.Client
    CacheManager *client.CacheManager
}
```

- **Shared cache**: All resources use the same cache instance
- **Automatic initialization**: Cache manager created during provider configuration

### 3. Resource Usage
```go
// Instead of individual API calls
server, err := client.GetServer(serverNumber)

// Use cached bulk data
servers, err := cacheManager.GetServers(client)
server, err := client.GetServerFromBulk(serverNumber, servers)
```

## Usage Examples

### Data Source: Get All Servers
```hcl
# Fetches ALL servers in one API call
data "hrobot_servers" "all" {}

# Use the data
output "server_count" {
  value = length(data.hrobot_servers.all.servers)
}

output "fsn1_servers" {
  value = [
    for server in data.hrobot_servers.all.servers : server
    if server.location == "FSN1"
  ]
}
```

### Resource: Server Order (Updated)
```hcl
resource "hrobot_server_order" "web" {
  product_id = "EX101"
  location   = "FSN1"
}

# The Read operation now uses bulk data instead of individual API calls
```

## Performance Benefits

| Scenario | Before | After | Improvement |
|----------|--------|-------|-------------|
| 10 servers | 10 calls/refresh | 1 call/apply | 90% reduction |
| 50 servers | 50 calls/refresh | 1 call/apply | 98% reduction |
| 100 servers | 100 calls/refresh | 1 call/apply | 99% reduction |
| 1000 servers | 1000 calls/refresh | 1 call/apply | 99.9% reduction |

## Rate Limit Impact

- **Before**: 100 servers = 100 calls = **2 hours max** before hitting rate limit
- **After**: 100 servers = 1 call = **200 applies per hour** possible

## Implementation Details

### Cache Lifecycle
1. **First access**: Cache manager calls `/server` endpoint
2. **Subsequent access**: Returns cached data
3. **Next apply**: Cache is reset, fresh data fetched

### Thread Safety
- **Read operations**: Multiple goroutines can read simultaneously
- **Write operations**: Single goroutine writes, others wait
- **Double-check locking**: Prevents race conditions

### Error Handling
- **API failures**: Properly propagated to resources
- **Cache misses**: Graceful fallback to individual API calls
- **Network issues**: Standard HTTP client retry logic

## Migration Guide

### Existing Resources
No changes needed! Existing resources automatically benefit from bulk fetching.

### New Data Sources
```hcl
# New: Bulk server data source
data "hrobot_servers" "all" {}

# Use in other resources
resource "example_resource" "test" {
  server_ip = data.hrobot_servers.all.servers[0].server_ip
}
```

## Best Practices

1. **Use data sources**: For read-only operations, use `hrobot_servers` data source
2. **Filter locally**: Use Terraform `for` expressions to filter server data
3. **Minimize API calls**: Let the cache manager handle bulk fetching
4. **Monitor usage**: Check Hetzner Robot dashboard for API usage

## Troubleshooting

### Cache Not Working
- Ensure provider is properly configured
- Check that resources are using `ProviderData` structure
- Verify cache manager is initialized

### Rate Limit Still Hit
- Check for resources making individual API calls
- Ensure all resources use the cache manager
- Monitor API usage in Hetzner Robot dashboard

### Performance Issues
- Cache is in-memory only (resets between applies)
- Large server counts may use more memory
- Consider filtering data sources for specific use cases
