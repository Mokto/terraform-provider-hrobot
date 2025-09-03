# Example demonstrating bulk server fetching with smart caching
# This fetches all servers in one API call and caches the result for the entire apply

terraform {
  required_providers {
    hrobot = {
      source  = "mokto/hrobot"
      version = "~> 0.1"
    }
  }
}

provider "hrobot" {
  username = var.hrobot_user   # or HROBOT_USERNAME env
  password = var.hrobot_pass   # or HROBOT_PASSWORD env
}

# Fetch all servers using bulk API call (only 1 API call per apply)
data "hrobot_servers" "all" {}

# Example: Get a specific server by number
locals {
  # Find server with number 123456 (if it exists)
  target_server = [
    for server in data.hrobot_servers.all.servers : server
    if server.server_number == 123456
  ]
  
  # Get all servers in FSN1 location
  fsn1_servers = [
    for server in data.hrobot_servers.all.servers : server
    if server.location == "FSN1"
  ]
  
  # Get all active servers
  active_servers = [
    for server in data.hrobot_servers.all.servers : server
    if server.status == "ready"
  ]
}

# Output all servers
output "all_servers" {
  value = data.hrobot_servers.all.servers
  description = "All servers fetched in one API call"
}

# Output specific server info
output "target_server" {
  value = length(local.target_server) > 0 ? local.target_server[0] : null
  description = "Server with number 123456 (if exists)"
}

# Output FSN1 servers
output "fsn1_servers" {
  value = local.fsn1_servers
  description = "All servers in FSN1 location"
}

# Output active servers
output "active_servers" {
  value = local.active_servers
  description = "All servers with 'ready' status"
}

# Example: Use server data in other resources
# resource "some_other_resource" "example" {
#   count = length(local.active_servers)
#   
#   server_ip = local.active_servers[count.index].server_ip
#   server_name = local.active_servers[count.index].server_name
# }
