# TalkIntent Hub Deployment on A4A

This directory contains the production deployment assets for TalkIntent Hub on the `A4A` production host (`62.234.91.42`), serving `https://talkintent.empeirion.cn`.

## Architecture & Design Facts

1. **Host & Isolation**:
   - Deployed on A4A alongside AsterGate and other services.
   - Fully isolated inside Docker using a custom `FROM scratch` image (zero external image pulls required).
   - Runs as non-root user `10001:10001` with data volume bound to `/opt/talkintent/data`.
   - Hub port binds strictly to `127.0.0.1:18800`.
   - Hub rate limiting: keyed on `asker.ID` (Bearer token member identity), completely independent of network `RemoteAddr` (`127.0.0.1`).

2. **TLS & Reverse Proxy**:
   - TLS certificate issued by the existing AsterGate Private CA (`/home/ubuntu/astergate-deploy/certs/ca.crt` and `ca.key`).
   - Private key stored at `/opt/talkintent/tls/talkintent.key` (permission `0600`).
   - Nginx server block at `/etc/nginx/conf.d/talkintent.conf`:
     - Port 80 HTTP redirects 301 to HTTPS.
     - Port 443 HTTPS reverse proxies `talkintent.empeirion.cn` to `127.0.0.1:18800`.
     - Supports WebSocket upgrades and long polling with 3600s proxy timeouts.
   - Nginx is reloaded via `systemctl reload nginx` only after `nginx -t` passes (never restarted or stopped).

3. **Production Safety Red Lines**:
   - Only paths created/modified: `/opt/talkintent/**`, `/etc/nginx/conf.d/talkintent.conf`, Docker container/image `talkintent*`, and backup dir `/root/talkintent-deploy-20260920-2330/`.
   - `ca.key` was only accessed by OpenSSL as a signing key; never copied, printed, or exfiltrated.
   - Admin token and private keys are never printed, logged, or included in commit history.
   - Full `/etc/nginx` tarball backed up prior to any Nginx reload.

## File Structure

- `Dockerfile`: Scratch-based container image with static `talkintent` binary and CA certificates bundle.
- `compose.yaml`: Docker compose specification for `talkintent-hub`.
- `talkintent.conf`: Nginx configuration snippet.
- `issue-cert.sh`: OpenSSL script to issue SAN certificate signed by AsterGate CA.
- `install.sh`: Automated deployment and verification script.
- `rollback.sh`: One-click rollback script.

## Rollback Procedure

To revert the deployment:
```bash
bash /root/talkintent-deploy-20260920-2330/rollback.sh
```
Or from this directory:
```bash
bash deploy/a4a/rollback.sh
```
This stops the Docker container, removes `/etc/nginx/conf.d/talkintent.conf`, tests Nginx syntax, and reloads Nginx cleanly.

## Team Member Onboarding

Team members can connect to the A4A TalkIntent deployment in four steps:

1. **Hosts Resolution**:
   Add the A4A IP and domain mapping to `/etc/hosts` (Linux/macOS) or `C:\Windows\System32\drivers\etc\hosts` (Windows):
   ```text
   62.234.91.42 talkintent.empeirion.cn
   ```

2. **Obtain CA Certificate**:
   Obtain the AsterGate Private CA root certificate (`ca.crt`) from the administrator and save it locally (e.g. `~/.talkintent/ca.crt`).

3. **Pair CLI with Private CA**:
   ```bash
   talkintent pair -hub https://talkintent.empeirion.cn -code <INVITE_CODE> -name <MACHINE_NAME> -hub-ca-file ~/.talkintent/ca.crt
   ```
   *Note: Without `-hub-ca-file`, TLS handshake fails with `x509: certificate signed by unknown authority`. Specifying `-hub-ca-file` validates the certificate and persists the absolute path in `~/.talkintent/config.json` for all subsequent commands.*

4. **Web UI Access**:
   Navigate to `https://talkintent.empeirion.cn/web`:
   - *Option A (Persistent)*: Import `ca.crt` into the operating system or browser trusted root store.
   - *Option B (Temporary)*: Click "Advanced" -> "Proceed to talkintent.empeirion.cn (unsafe)" to bypass the private CA warning.

## Verification & Audit Evidence

The deployment and end-to-end operations were rigorously verified with zero secret leaks:
- **Audit Evidence (`C:/tmp/ti-a4a/audit/`)**: 30 distinct verification artifacts confirming Docker isolation (scratch image, UID 10001:10001, port 127.0.0.1:18800), rate limiting decoupled from loopback IP, Nginx syntax and reverse proxy config, AsterGate containers and endpoints untouched, backup archives intact, and admin token length 56 bytes.
- **End-to-End Verification (`C:/tmp/ti-a4a/verify/`)**: 9 test artifacts confirming x509 validation rejection without CA, HTTPS pairing for alice and bob, WebSocket daemon connection (`wss://talkintent.empeirion.cn/ws/daemon`), member discovery, live query execution with `git_diff` and `git_status`, and clean post-test teardown.

