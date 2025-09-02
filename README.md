# Terraform / OpenTofu Provider for Hetzner Robot

![build](https://github.com/mokto/terraform-provider-hrobot/actions/workflows/test.yml/badge.svg)
![release](https://github.com/mokto/terraform-provider-hrobot/actions/workflows/release.yml/badge.svg)

A [Terraform](https://www.terraform.io) / [OpenTofu](https://opentofu.org) provider to automate [Hetzner Robot](https://robot.hetzner.com/) bare-metal servers.

⚠️ **Status:** experimental, not affiliated with Hetzner.

---

## Features

- **Order servers** via `hrobot_server_order` resource (returns a transaction id).
- **Inspect order transactions** via `hrobot_order_transaction` data source (status, server number, IP).
- **Install operating systems** via `hrobot_installimage` resource:
  - activate Rescue
  - reboot
  - upload an `autosetup` file
  - run `installimage`
  - optional `post-install` or `ansible-pull`.

---

## Installation

### OpenTofu (recommended)

Add to your `.tf`:

```hcl
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
```

#### Order a server

```hcl
resource "hrobot_server_order" "ex101" {
  product_id = "EX101"
  location   = "FSN1"

  # Use SSH keys already uploaded in Hetzner Robot
  authorized_key_fingerprints = [var.robot_key_fp]
}

output "order_transaction_id" {
  value = hrobot_server_order.ex101.transaction_id
}
```

At this stage, the order has been placed but the server may take hours/days to be “ready”.


#### Inspect the order transaction

```hcl
data "hrobot_order_transaction" "ex101" {
  transaction_id = hrobot_server_order.ex101.transaction_id
}

output "server_status" {
  value = data.hrobot_order_transaction.ex101.status
}

output "server_number" {
  value = data.hrobot_order_transaction.ex101.server_number
}

output "server_ip" {
  value = data.hrobot_order_transaction.ex101.server_ip
}
```

server_status  = "in process" or "ready"


#### Configure a server

```hcl
resource "hrobot_configuration" "web_server" {
  server_number = 123456  # Replace with your actual server number
  server_name   = "web-server-01"
  
  # Autosetup configuration for Ubuntu 22.04
  autosetup_content = <<-EOT
    #!/bin/bash
    # Hetzner Online GmbH - installimage
    
    # Set the hostname
    HOSTNAME web-server-01
    
    # Set the root password
    ROOT_PASSWORD your-secure-password
    
    # Configure the network
    INTERFACE 0
    IP_ADDR 192.168.1.100
    NETMASK 255.255.255.0
    GATEWAY 192.168.1.1
    
    # Install Ubuntu 22.04
    DISTRIBUTION ubuntu
    VERSION 22.04
    
    # Configure the disk
    DRIVE1 /dev/sda
    PART /boot ext4 512M
    PART swap swap 4G
    PART / ext4 all
    
    # Configure SSH
    SSH_PUBLIC_KEY your-ssh-public-key
    
    # Run post-install script
    POSTINSTALL_SCRIPT /root/post-install.sh
  EOT
  
  # Post-install script to configure the server
  post_install_content = <<-EOT
    #!/bin/bash
    set -euo pipefail
    
    # Update the system
    apt-get update
    apt-get upgrade -y
    
    # Install common packages
    apt-get install -y nginx ufw fail2ban
    
    # Configure firewall
    ufw allow ssh
    ufw allow 'Nginx Full'
    ufw --force enable
    
    # Configure fail2ban
    systemctl enable fail2ban
    systemctl start fail2ban
    
    # Start nginx
    systemctl enable nginx
    systemctl start nginx
    
    echo "Server configuration completed successfully!"
  EOT
  
  # SSH key fingerprints for rescue mode access
  rescue_authorized_key_fingerprints = [
    "your-ssh-key-fingerprint-1",
    "your-ssh-key-fingerprint-2"
  ]
  
  # Timeout for SSH connection (in minutes)
  ssh_wait_timeout_minutes = 30
}
```


```hcl
resource "hrobot_vswitch" "internal_network" {
  vlan = 4000
  name = "internal-network"
}
```

## License

MIT — see [LICENSE](LICENSE).
