# Terraform / OpenTofu Provider for Hetzner Robot

![build](https://github.com/mokto/terraform-provider-hrobot/actions/workflows/test.yml/badge.svg)
![release](https://github.com/mokto/terraform-provider-hrobot/actions/workflows/release.yml/badge.svg)

A [Terraform](https://www.terraform.io) / [OpenTofu](https://opentofu.org) provider to automate [Hetzner Robot](https://robot.hetzner.com/) bare-metal servers.

⚠️ **Status:** experimental, not affiliated with Hetzner.

---

## Features

- **Order servers** via `hrobot_server_order` resource (returns a transaction id).
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

  addons = ["primary_ipv4"]

  # Use SSH keys already uploaded in Hetzner Robot
  authorized_key_fingerprints = [var.robot_key_fp]
}

output "order_transaction_id" {
  value = hrobot_server_order.ex101.transaction_id
}
```

At this stage, the order has been placed but the server may take hours/days to be “ready”.


server_status  = "in process" or "ready"


#### Configure a server

```hcl
resource "hrobot_configuration" "web_server" {
  server_number = hrobot_server_order.test.server_number  # Replace with your actual server number
  server_name   = "web-server-01"
  count         = hrobot_server_order.test.status == "ready" ? 1 : 0

  # Autosetup configuration for Ubuntu 22.04
  autosetup_content = <<-EOT
  DRIVE1 /dev/sda
  BOOTLOADER grub
  PART /boot/efi esp 256M
  PART /boot ext4 1G
  PART /     ext4 all crypt
  IMAGE /root/images/Ubuntu-2404-noble-amd64-base.tar.gz
  HOSTNAME coucou
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
