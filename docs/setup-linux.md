# Linux setup

Tested on Ubuntu 22.04/24.04 and Debian 12. Any distro with Docker works; the Ansible roles target Debian-family systems.

## Automated (Ansible)

```bash
sudo apt-get update && sudo apt-get install -y ansible git
git clone https://github.com/<you>/titanedge.git && cd titanedge
ansible-galaxy collection install -r ansible/requirements.yml
ansible-playbook -i ansible/inventories/local/hosts.yml ansible/playbooks/workstation.yml -K
newgrp docker          # or log out/in so your user can talk to Docker
```

Installs Docker Engine, kind, kubectl, Helm, Terraform and Go at the versions in `ansible/playbooks/group_vars/all.yml`, and applies the kernel tuning in [`roles/kernel_tuning`](../ansible/roles/kernel_tuning/defaults/main.yml) (ephemeral port range, somaxconn, file descriptors, inotify limits that Kind needs for many pods).

To go all the way to a running platform:

```bash
ansible-playbook ansible/playbooks/site.yml -K -e profile=lite
```

## Manual

```bash
# Docker Engine
curl -fsSL https://get.docker.com | sh && sudo usermod -aG docker "$USER"

# kind, kubectl, helm
curl -Lo kind https://kind.sigs.k8s.io/dl/v0.33.0/kind-linux-amd64 && sudo install kind /usr/local/bin/
curl -LO "https://dl.k8s.io/release/v1.36.1/bin/linux/amd64/kubectl" && sudo install kubectl /usr/local/bin/
curl -fsSL https://get.helm.sh/helm-v4.3.0-linux-amd64.tar.gz | tar -xz && sudo install linux-amd64/helm /usr/local/bin/

# Terraform, Go
sudo apt-get install -y terraform   # from the HashiCorp apt repo, or download the zip
curl -fsSL https://go.dev/dl/go1.27.1.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
```

Kind with many pods needs higher inotify limits:

```bash
sudo sysctl fs.inotify.max_user_watches=1048576 fs.inotify.max_user_instances=8192
```

## Run

```bash
make up                # lite
make up PROFILE=full   # + Istio, Argo CD, Jenkins
```

## Validate

```bash
kubectl get pods -A
kubectl get hpa -n titanedge
kubectl get scaledobjects -n titanedge
scripts/smoke.sh
```
