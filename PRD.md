# Product Requirements Document (PRD) - Hiddify Usage Engine (HUE)

## 1. Overview
Hiddify Usage Engine (HUE) is a protocol-agnostic usage tracking and subscription control plane. It is designed to manage user consumption, enforce package limits, and provide granular reporting across multiple nodes and various protocols (**Xray/Singbox/PPP/PPTP/SSTP/L2TP/IPSec/OpenVPN/Vless/Trojan/Shadowsocks/VMess/WireGuard**) without being tied to a specific panel or service.

## 2. Core Entities & Data Model

### 2.1 User
- **UUID**: Unique identifier.
- **Username**: Plain text.
- **Password**: Plain text (required for service authentications).
- **Public Key & Private Key**: Used for cryptographic auth (e.g., WireGuard, SSH). Stored in plain text for provisioning.   
- **CA Cert List**: List of strings. If empty, no restriction.
- **Groups**: List of strings.
- **Allowed Device IDs**: List of identifiers. If empty, no restriction.
- **Status**: `active`, `suspended`, `expired`, `finish`, `inactive` (manual).
- **Active Package ID**: Current subscription link.
- **First Connection At**: Timestamp.
- **Last Connection**: Timestamp (updated every usage report).

### 2.2 Package
- **UUID**: Unique identifier.
- **User ID**: Owner.
- **Total Traffic (Bytes)**: Combined limit.
- **Upload/Download Limits (Bytes)**: Optional separate limits.
- **Reset Usage Mode**: `no-reset`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`.
- **Duration (Seconds)**: Expiry period.
- **Start At**: Optional fix. If empty, activates on first connection.
- **Max Concurrent**: Number of allowed simultaneous IP-based sessions.
- **Status**: `active`, `expired`, `finish`, `suspended`.

### 2.3 Node
Any server hosting services.
- **UUID**: Unique identifier.
- **Secret Key**: For authentication.
- **Name**: User-friendly label.
- **Allowed IPs**: Whitelist.
- **Traffic Multiplier**: Usage scaling factor (e.g., multiplier 2).
- **Reset Mode**: `no-reset`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`.
- **Reset Day**: Scheduled reset point.
- **Current Upload & Download**: Aggregate counters.
- **Geo Information**: country, city, isp.

- **History Storage**: Node usage history is stored in a separate db.


### 2.4 Service
Specific protocol instance on a Node.
- **UUID / Secret Key**: For authentication.
- **Node ID**: Parent node identification.
- **Allowed Auth Methods**: [`uuid`, `password`, `pubkey`, etc.] sent to the service.
- **Callback URL** (optional): For pushing real-time usage.

- **History Storage**: Service usage history is stored in a separate db.

## 3. Functional Requirements

### 3.1 Usage Tracking
- **Unified Reporting**: Standardized format for ALL protocols.
- **Reporting Intervals**: Services push or Core pulls usage every $N$ seconds/minutes.
- **Granular Tagging**: Events include `tags` (vless, wireguard), `service` (xray-usage-service), and `node` IDs.
- **Geo Extraction**: Raw IPs are used for session counting and Geo-metadata (MaxMind) extraction, then discarded. **Zero Raw-IP Retention** policy items are deleted immediately after processing without any logging.

### 3.2 Quota & Enforcement
- **Hard Limits**: On quota breach, status becomes `suspended`. Disconnect events are **batched** for performance.
- **Concurrent Session Enforcement**: Unique IPs active within $X$ seconds are counted.
- **Penalty Logic**: Exceeding `max_concurrent` triggers a temporary penalty (disconnect for $N$ minutes), logged but not permanent in DB.
- **Locking Model**: Fine-grained locking. Locks apply only to the specific service, node, or user being modified, never the entire system.

### 3.3 Event Sourcing Model
All state changes are captured as events for audit, replay, and consistency:
- `USER_CONNECTED`, `USER_DISCONNECTED`
- `USAGE_RECORDED`
- `PACKAGE_EXPIRED` / `PACKAGE_UPDATED`
- `NODE_RESET`

## 4. Technical Architecture

### 4.1 Performance & Overhead
- **Efficiency Goal**: Extremely low CPU/Memory footprint. Small deployments (100 users) must run without heavy database overhead.
- **Data Persistence**: 
    - **Usage Data**: Cached in-memory. Loss of 5 minutes of usage is acceptable.
    - **Metadata Changes**: (Quota changes, status updates) must be written to DB **immediately**.
- **Configuration**: All settings configurable via **Environment Variables** (ENV).

### 4.2 Scalability & Storage
- **Small Scale**: Multi-threaded single instance. Uses a file-based timeseries DB (e.g. SQLite with WAL or custom file logs).
- **Large Scale**: Highly-available multi-instance Core. External timeseries DB + Redis for real-time counters.
- **Optimized Tables**: Suggested separation of **Today's Usage** (high access) from **Historical Archive**.

### 4.3 Communication
- **TLS Mandatory**: All Node <-> Core communication must be encrypted via TLS.
- **Pull/Push Hybrid**: Core can request (pull) current consumption from all services at any time.

## 5. Security Summary
- **Encrypted Communication**: TLS for all endpoints.
- **Strict IP Handling**: Immediate deletion of raw IPs after metadata extraction.
