# Example demonstrating autosetup parameters
# This shows how to use the new name, arch, and cryptpassword parameters

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

# Example 1: Using autosetup parameters (recommended)
resource "hrobot_configuration" "web_server_amd64" {
  server_number = 123456  # Replace with your actual server number
  server_ip     = "1.2.3.4"  # Replace with your server's IP address
  server_name   = "web-server-01"

  # Required autosetup parameters
  arch          = "amd64"
  cryptpassword = "my-secure-password-123"

  # Optional: Custom post-install script
  post_install_content = <<-EOT
    #!/bin/bash
    set -euo pipefail
    
    # Update system
    apt-get update && apt-get upgrade -y
    
    # Install nginx
    apt-get install -y nginx
    
    # Start nginx
    systemctl enable nginx
    systemctl start nginx
    
    echo "Web server setup completed!"
  EOT

  # SSH key fingerprints for rescue mode access
  rescue_authorized_key_fingerprints = [
    "your-ssh-key-fingerprint-here"
  ]
}

# Example 2: ARM64 server with minimal configuration
resource "hrobot_configuration" "arm_server" {
  server_number = 123457  # Replace with your actual server number
  server_ip     = "1.2.3.5"  # Replace with your server's IP address
  server_name   = "arm-server"

  # Required parameters
  arch          = "arm64"
  cryptpassword = "my-secure-password-456"
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

# Outputs
output "web_server_ip" {
  value       = hrobot_configuration.web_server_amd64.server_ip
  description = "IP address of the AMD64 web server"
}

output "arm_server_ip" {
  value       = hrobot_configuration.arm_server.server_ip
  description = "IP address of the ARM64 server"
}

