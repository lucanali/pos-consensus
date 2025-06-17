# Simplified Proof-of-Stake (PoS) Consensus Algorithm with P2P Gossip

This is a simplified implementation of a Proof-of-Stake consensus algorithm written in Go with real peer-to-peer communication using libp2p. The implementation focuses on the core consensus mechanism with distributed network communication.

## Features

- Multiple validator nodes (separate instances with real network communication)
- Stake-based validator selection
- Time-based randomness in leader selection  
- Block proposal and validation
- **P2P Communication** using libp2p
- **Real network consensus** (no simulation)
- Message deduplication and TTL handling
- Network fault tolerance and heartbeat monitoring
- JSON-based message format
- Simple blockchain state management
- Cryptographic key generation and validation

## Architecture

Each validator runs as a separate instance with:
- **libp2p Host**: Handles P2P networking and message routing
- **Gossip Protocol**: Broadcasts messages (proposals, votes) across the network
- **Consensus Engine**: Participates in distributed block validation and voting

## Key Components

### 1. Validator
- **ID**: Derived from public key
- **Address**: libp2p multiaddr for P2P communication
- **PublicKey/PrivateKey**: Cryptographic identity
- **Stake**: Voting power in consensus
- **IsOnline**: Real-time availability status
- **LastSeen**: Network activity tracking

### 2. P2P Gossip Protocol
- **MessageTypes**: BlockProposal, Vote, PeerAnnounce, Heartbeat
- **TTL**: Time-to-live for message propagation control
- **Signature**: Message authentication
- **Deduplication**: Prevents message loops
- **Fault Tolerance**: Handles network failures gracefully

### 3. Network Communication
- **libp2p**: Modern P2P networking stack
- **Async Processing**: Non-blocking message handling
- **Health Monitoring**: Heartbeat and offline detection
- **DHT Support**: While not currently implemented, the system can be enhanced with libp2p's DHT (Distributed Hash Table) for automatic peer discovery. This would eliminate the need for manual peer ID specification, allowing nodes to automatically discover and connect to other validators in the network. DHT would provide:
  - Automatic peer discovery without manual configuration
  - Dynamic network topology management
  - Improved network resilience and scalability
  - Bootstrap node discovery for new validators

> **Reference Implementation**: The system can be enhanced using [go-libp2p-kad-dht](https://github.com/libp2p/go-libp2p-kad-dht), which provides a Kademlia DHT implementation for libp2p. This implementation follows the [libp2p DHT specification](https://github.com/libp2p/specs/tree/master/kad-dht) and offers robust peer discovery capabilities.

## Consensus Process

1. **Network Formation**: Validators discover peers through announcements
2. **Leader Selection**: Stake-weighted random selection across network
3. **Block Proposal**: Leader broadcasts proposal via gossip protocol
4. **Distributed Validation**: Each validator validates independently
5. **Vote Broadcasting**: Validators gossip their votes across network
6. **Consensus Achievement**: Block accepted when >50% of total stake approves
7. **Chain Synchronization**: All validators update their blockchain state

## Usage

### Build the application:
```bash
go build -o pos-validator
```

### Run Network of Validators:

#### Terminal 1 (First validator):
```bash
./pos-validator -port=8080 -stake=150
```

#### Terminal 2 (Second validator, connects to first):
```bash
./pos-validator -port=8081 -stake=200 -peers=<peer id of node 1>
```

#### Terminal 3 (Third validator, connects to network):
```bash
./pos-validator -port=8082 -stake=80 -peers=<peer addres of node 1>,<peer addres of node 2>
```

#### Terminal 4 (Fourth validator):
```bash
./pos-validator -port=8083 -stake=120 -peers=<peer addres of node 1>,<peer addres of node 2>,<peer addres of node 3>
```

### Command Line Options:
- `-port`: Port number for this validator's libp2p host (default: 8080)
- `-stake`: Stake amount for this validator (default: 100)
- `-peers`: Comma-separated list of peer libp2p multiaddrs to connect to
- `-peerid`: Unique ID for this peer (optional, for deterministic key generation)

## Example Network Communication

```
Starting PoS Validator Node with P2P Gossip
Validator ID: a1b2c3d4e5f6g7h8
Stake: 150
Address: /ip4/127.0.0.1/tcp/8080/p2p/QmBootstrapNodeID

Starting libp2p host on port 8080
Connecting to peers: [/ip4/127.0.0.1/tcp/8081/p2p/QmSecondNodeID]
Added peer: b2c3d4e5f6g7h8i9 (/ip4/127.0.0.1/tcp/8081/p2p/QmSecondNodeID) with stake 200
Discovered new peer: c3d4e5f6g7h8i9j0 (/ip4/127.0.0.1/tcp/8082/p2p/QmThirdNodeID) with stake 80

Starting new consensus round...
Leader selected: b2c3d4e5f6g7h8i9 (Stake: 200)
Received block proposal: Index 0 from b2c3d4e5f6g7h8i9
 Vote cast by a1b2c3d4e5f6g7h8: true for block 7f3a2b1c9d8e5f...
Received vote from c3d4e5f6g7h8i9j0: true for proposal 7f3a2b1c9d8e5f...
Consensus reached! Block 0 accepted with 430/530 stake
Block added to chain. Chain length: 1
```

## Message Types


### 1. Block Proposal
```json
{
  "type": "block_proposal",
  "from": "validator_id",
  "payload": {
    "index": 0,
    "data": "Block_0_by_validator_id",
    "hash": "7f3a2b1c9d8e5f...",
    "validator": "validator_id"
  }
}
```

### 2. Vote
```json
{
  "type": "vote", 
  "from": "validator_id",
  "payload": {
    "proposal_hash": "7f3a2b1c9d8e5f...",
    "validator_id": "voter_id",
    "approve": true
  }
}
```

### 3. Peer Announcement
```json
{
  "type": "peer_announce",
  "from": "validator_id",
  "payload": {
    "id": "validator_id",
    "address": "/ip4/127.0.0.1/tcp/8080/p2p/QmNodeID",
    "stake": 150
  }
}
```

## Network Resilience

- **Message Deduplication**: Prevents processing duplicate messages
- **TTL (Time-to-Live)**: Limits gossip propagation depth
- **Peer Health Monitoring**: Heartbeat messages and offline detection
- **Graceful Failure Handling**: Continues operation when peers go offline
- **Automatic Recovery**: Reconnects to peers when they come back online

## Key Algorithms

### Gossip Protocol
```go
// Broadcast message to all connected peers
for peer := range connectedPeers {
    go sendMessageToPeer(peer.address, message)
}
```

### Consensus Validation
```go
// Distributed consensus requires majority stake approval
requiredStake := totalNetworkStake / 2 + 1
consensus := votedStake >= requiredStake
```

## Security Features

- **Cryptographic Signatures**: All messages signed with validator's private key
- **Message Authentication**: Prevents tampering and spoofing
- **Stake-based Sybil Resistance**: Higher stake = more influence
- **Network Verification**: Block validation by multiple independent validators
- **Timestamp Validation**: Prevents future-dated blocks

## Testing the P2P Network

1. **Start validators in sequence** with peer connections
2. **Watch network formation** as peers discover each other
3. **Observe leader selection** rotating based on stake weights
4. **Monitor consensus process** with real vote broadcasting
5. **Test fault tolerance** by stopping/starting validators
6. **Verify chain consistency** across all network participants

## Quick Start

Use the provided scripts to quickly start and test the network:

`start_network.sh`: Starts a 4-validator network with different stake amounts

Example:
```bash
# Start the network
./start_network.sh
```

This implementation provides a realistic demonstration of how Proof-of-Stake consensus works in a distributed network environment with real peer-to-peer communication using libp2p!