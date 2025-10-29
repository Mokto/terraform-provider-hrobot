# Example demonstrating autosetup parameters
# This shows how to use the name, arch, and cryptpassword parameters
# The postinstall script for LUKS encryption setup is now built-in

terraform {
  required_providers {
    hrobot = {
      source  = "mokto/hrobot"
      version = "~> 0.1"
    }
  }
}

provider "hrobot" {
  username = var.hrobot_user # or HROBOT_USERNAME env
  password = var.hrobot_pass # or HROBOT_PASSWORD env
}

# Example 1: Using autosetup parameters with K3S (recommended)
resource "hrobot_configuration" "web_server_amd64" {
  server_number = 123456    # Replace with your actual server number
  server_ip     = "1.2.3.4" # Replace with your server's IP address
  server_name   = "web-server-01"

  # Required autosetup parameters
  arch          = "amd64"
  cryptpassword = "my-secure-password-123"

  # K3S parameters
  k3s_token = var.k3s_token                           # K3S cluster token
  k3s_url   = "https://${var.master_private_ip}:6443" # K3S server URL

  # Node labels
  node_labels = [
    {
      name  = "dedicated"
      value = "true"
    },
    {
      name  = "storage"
      value = "true"
    }
  ]

  # Taints
  taints = [
    "localstorage=true:NoSchedule"
  ]

  # Optional: Install Docker (default: false)
  install_docker = true

  # SSH key fingerprints for rescue mode access
  rescue_authorized_key_fingerprints = [
    "your-ssh-key-fingerprint-here"
  ]
}

# Example 2: ARM64 server with minimal configuration
resource "hrobot_configuration" "arm_server" {
  server_number = 123457    # Replace with your actual server number
  server_ip     = "1.2.3.5" # Replace with your server's IP address
  server_name   = "arm-server"

  # Required parameters
  arch          = "arm64"
  cryptpassword = "my-secure-password-456"

  # K3S parameters are required
  k3s_token = var.k3s_token
  k3s_url   = "https://${var.master_private_ip}:6443"

  # SSH key fingerprints for rescue mode access
  rescue_authorized_key_fingerprints = [
    "your-ssh-key-fingerprint-here"
  ]
}


# Variables
variable "hrobot_user" {
  description = "Hetzner Robot username"
  type        = string
  sensitive   = true
}

variable "hrobot_pass" {
  description = "Hetzner Robot password"
  type        = string
  sensitive   = true
}

variable "k3s_token" {
  description = "K3S cluster token"
  type        = string
  sensitive   = true
}

variable "master_private_ip" {
  description = "Private IP address of the K3S master node"
  type        = string
}

# Outputs
output "web_server_ip" {
  value       = hrobot_configuration.web_server_amd64.server_ip
  description = "IP address of the AMD64 web server"
}

output "arm_server_ip" {
  value       = hrobot_configuration.arm_server.server_ip
  description = "IP address of the ARM64 server"
}
