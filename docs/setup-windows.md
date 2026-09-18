# Windows setup

TitanEdge runs on Windows 10/11 through Docker Desktop's WSL2 backend. The scripts are Bash; run them from a WSL2 Ubuntu shell (recommended) or Git Bash.

## 1. Install the tools

Open **PowerShell as Administrator**:

```powershell
wsl --install -d Ubuntu            # reboot if asked, then create your Linux user
winget install -e --id Docker.DockerDesktop
winget install -e --id Kubernetes.kind
winget install -e --id Kubernetes.kubectl
winget install -e --id Helm.Helm
winget install -e --id Hashicorp.Terraform
winget install -e --id GoLang.Go
winget install -e --id OpenJS.NodeJS.LTS
winget install -e --id GnuWin32.Make   # optional; `make` targets also work inside WSL
```

## 2. Configure Docker Desktop

Settings → **Resources**:

| Profile | CPUs | Memory | Disk |
|---|---|---|---|
| compose | 4 | 4 GB | 30 GB |
| Kind `lite` | 6 | 8 GB | 60 GB |
| Kind `full` (Istio, Argo CD, Jenkins) | 8 | 12-16 GB | 80 GB |

Settings → Resources → **WSL integration** → enable your Ubuntu distro.

With the WSL2 backend the memory limit lives in `%UserProfile%\.wslconfig`:

```ini
[wsl2]
memory=12GB
processors=8
swap=4GB
```

Then `wsl --shutdown` and restart Docker Desktop.

## 3. Tools inside WSL2 (recommended path)

```bash
# in Ubuntu (WSL2)
git clone https://github.com/<you>/titanedge.git && cd titanedge
sudo apt-get update && sudo apt-get install -y ansible
ansible-playbook ansible/playbooks/workstation.yml -K   # kind, kubectl, helm, terraform, go, kernel limits
```

The Ansible role detects Docker Desktop's CLI and does not install a second Docker Engine.

Keep the repository **inside the WSL filesystem** (`~/titanedge`), not under `/mnt/c` - bind mounts and Go builds are an order of magnitude faster there.

## 4. Run it

```bash
make up                       # or: scripts/up.sh --profile lite
make up PROFILE=full
```

From PowerShell/Git Bash the same works directly:

```powershell
bash scripts/up.sh --profile lite
```

Open http://localhost:8080. Ports 8080, 3000, 9090, 16686, 8443 and 8081 on the Windows host are forwarded into the Kind control-plane container.

## 5. Load test from Windows

`titanload` is a native Windows binary too:

```powershell
go build -o titanload.exe ./cmd/titanload
.\titanload.exe run -u http://localhost:8080/api/v1/ping -c 256 -d 60s
```

The live dashboard needs a VT-capable terminal (Windows Terminal, VS Code). In legacy `conhost` titanload enables virtual-terminal mode automatically; add `--no-dashboard` if the output still looks garbled.

## Windows-specific notes

- **Ephemeral ports.** Windows defaults to ~16k ephemeral ports. For very high connection churn (`--no-keepalive`) raise it:
  `netsh int ipv4 set dynamicport tcp start=10000 num=55535` (admin).
- **Docker Desktop NAT** adds latency and caps throughput around a few tens of thousands of requests/second on a laptop. That is the machine, not the platform - see [scaling-to-1m-rps.md](scaling-to-1m-rps.md).
- **Line endings.** `.gitattributes` forces LF for scripts and YAML; if you cloned before it existed run `git add --renormalize .`.
