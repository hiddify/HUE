# Product Requirements Document (PRD) - Hiddify Usage Engine (HUE)

## 1. Overview
Hiddify Usage Engine (HUE) is a protocol-agnostic usage tracking and subscription control plane. It is designed to manage user consumption, enforce package limits, and provide granular reporting across multiple services and various protocols (**Xray/Singbox/PPP/PPTP/SSTP/L2TP/IPSec/OpenVPN/Vless/Trojan/Shadowsocks/VMess/WireGuard/RADIUS**) without being tied to a specific panel or service.

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

### 2.3 Node (Logical Entity)
In HUE, a Node is a logical grouping of services, typically identified by its IP address.
- **IPs**: Node IP addresses.
- **Name**: User-friendly label.
- **Traffic Multiplier**: Usage scaling factor (e.g., multiplier 2).
- **Reset Mode**: `no-reset`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`.
- **Reset Day**: Scheduled reset point.
- **Current Upload & Download**: Aggregate counters.
- **Geo Information**: country, city, isp.
- **History Storage**: Node usage history is stored in a **separate database**.

### 2.4 Service
Specific protocol instance connecting to the Core.
- **UUID / Secret Key**: For authentication.
- **Allowed Auth Methods**: [`uuid`, `password`, `pubkey`, etc.] visible to the service.
- **Callback URL** (optional): For pushing real-time usage.
- **History Storage**: Service usage history is stored in a **separate database**.

## 3. Functional Requirements

### 3.1 Usage Tracking
- **Unified Reporting**: Standardized format for ALL protocols.
- **Reporting Intervals**: Services push or Core pulls usage every $N$ seconds/minutes.
- **Granular Tagging**: Events include `tags` (e.g., `vless`, `wireguard`). `service` and `node` properties are determined automatically by the Core based on the connection source.
- **Geo Extraction**: Raw IPs are used for session counting and Geo-metadata (MaxMind) extraction, then discarded. **Zero Raw-IP Retention** policy: item is deleted immediately after processing without any logging.

### 3.2 Quota & Enforcement
- **Hard Limits**: On quota breach, status becomes `suspended`. Disconnect events are **batched** for performance.
- **Concurrent Session Enforcement**: Unique IPs active within $X$ seconds are counted.
- **Penalty Logic**: Exceeding `max_concurrent` triggers a temporary penalty (disconnect for $N$ minutes), logged in-memory but not permanent in DB.
- **Locking Model**: Fine-grained locking. Locks apply only to the specific service or user being modified.

### 3.3 Event Sourcing Model
All state changes are captured as events for audit and consistency:
- `USER_CONNECTED`, `USER_DISCONNECTED`
- `USAGE_RECORDED`, `PACKAGE_EXPIRED`, `NODE_RESET`.

## 4. Technical Architecture & Database Strategy

### 4.1 Low-Overhead Database Strategy (for 1000+ Users)
To achieve high speed, low memory footprint, and minimal I/O:
- **Database Separation**:
    - **UserDB (SQLite)**: Stores only metadata, current status, and active counters. Small size ensures high-speed lookups and low I/O.
    - **Active DB (SQLite/WAL)**: Stores Temporary usage history data in memory and flushed to the Active DB in batches (e.g., every n minutes). Loss of n mins of usage is acceptable for performance.
    - **History DB (time-based)**: Stores usage logs and event history. This prevents historical data growth from slowing down core lookups.
- **I/O Optimization**:
    - **Buffered Writes**: Usage data is aggregated in memory and flushed to the Active DB in batches (e.g., every 5 minutes). Loss of 5 mins of usage is acceptable for performance.
    - **Write-Ahead Logging (WAL)**: Used for concurrent read/write efficiency in SQLite.
- **Memory Footprint**:
    - **In-Memory Cache**: Active user status and current session IPs are kept in memory for $O(1)$ enforcement checks.
    - **Prepared Statements**: Minimizes CPU overhead for repeated SQL operations.

### 4.2 Communication & Auth
- **gRPC Metadata Auth**: `access token` (JWT or any other token)‍‍ header are passed in gRPC Headers (Metadata) for every request. This reduces message overhead. based on the access token the system will identify whether the sender is a service or manager or ....
- **Persistent Stream**: A long-lived gRPC stream (`EnforcementService`) is used by services to receive real-time commands (like Disconnect) from the Core.
- **TLS Mandatory**: Secure communication for all protocols.

### 4.3 Configuration
- **Cloud-Native**: Fully configurable via **Environment Variables** (ENV).

### 4.4 Node Communication
- **TLS Mandatory**: Secure communication for all protocols.
- **RADIUS (Last Priority)**: Support for Mikrotik/NAS via RADIUS protocol will be implemented as the final phase.

## 5. Security Summary
- **Encrypted Communication**: TLS for all endpoints.
- **Strict IP Handling**: Immediate deletion of raw IPs after metadata extraction.
- **Fine-grained Authorization**: Access controlled per Service.