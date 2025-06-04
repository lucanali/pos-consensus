package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"bytes"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"github.com/sirupsen/logrus"
)

var logger *logrus.Logger

// MessageType represents different types of gossip messages
type MessageType string

const (
	MessageTypeBlockProposal MessageType = "block_proposal"
	MessageTypeVote          MessageType = "vote"
	MessageTypePeerAnnounce  MessageType = "peer_announce"
	MessageTypeHeartbeat     MessageType = "heartbeat"
	RequestChainLength       MessageType = "request_chain_length"
	RespondChainLength       MessageType = "respond_chain_length"
	RequestBlockchain        MessageType = "request_blockchain"
	RespondBlockchain        MessageType = "respond_blockchain"
	RequestSpecificBlock     MessageType = "request_specific_block"
	RespondSpecificBlock     MessageType = "respond_specific_block"
	AnnouncementCooldown                 = 30 * time.Second // Add this constant
)

// Validator represents a validator node in the network
type Validator struct {
	ID         string    `json:"id"`
	Address    string    `json:"address"`
	PublicKey  string    `json:"public_key"`
	PrivateKey string    `json:"private_key,omitempty"`
	Stake      int64     `json:"stake"`
	IsOnline   bool      `json:"is_online"`
	LastSeen   time.Time `json:"last_seen"`
}

// Block represents a block in the blockchain
type Block struct {
	Index        int64     `json:"index"`
	Timestamp    time.Time `json:"timestamp"`
	Data         string    `json:"data"`
	PreviousHash string    `json:"previous_hash"`
	Hash         string    `json:"hash"`
	Validator    string    `json:"validator"`
	Signature    string    `json:"signature"`
}

// BlockProposal represents a proposed block with voting info
type BlockProposal struct {
	Block      Block           `json:"block"`
	Proposer   string          `json:"proposer"`
	Votes      map[string]bool `json:"votes"`
	VoteCount  int             `json:"vote_count"`
	TotalStake int64           `json:"total_stake"`
	VotedStake int64           `json:"voted_stake"`
	Timestamp  time.Time       `json:"timestamp"`
}

// Vote represents a validator's vote on a block proposal
type Vote struct {
	ProposalHash string    `json:"proposal_hash"`
	ValidatorID  string    `json:"validator_id"`
	Approve      bool      `json:"approve"`
	Timestamp    time.Time `json:"timestamp"`
	Signature    string    `json:"signature"`
}

// GossipMessage represents a message in the gossip protocol
type GossipMessage struct {
	Type      MessageType `json:"type"`
	From      string      `json:"from"`
	To        string      `json:"to,omitempty"` // Empty for broadcast
	Timestamp time.Time   `json:"timestamp"`
	MessageID string      `json:"message_id"`
	TTL       int         `json:"ttl"` // Time to live for gossip propagation
	Payload   interface{} `json:"payload"`
	Signature string      `json:"signature"`
}

// Network represents the PoS network with P2P gossip
type Network struct {
	Validators        map[string]*Validator     `json:"validators"`
	Blockchain        []*Block                  `json:"blockchain"`
	CurrentLeader     string                    `json:"current_leader"`
	Proposals         map[string]*BlockProposal `json:"proposals"`
	Port              string                    `json:"port"`
	Address           string                    `json:"address"`
	MyValidator       *Validator                `json:"my_validator"`
	Peers             map[string]*Validator     `json:"peers"`         // Connected peers
	SeenMessages      map[string]time.Time      `json:"seen_messages"` // Message deduplication
	host              host.Host                 // libp2p host
	ctx               context.Context           // context for libp2p operations
	mu                sync.RWMutex
	messageMu         sync.RWMutex         // Separate mutex for message handling
	lastAnnouncements map[string]time.Time // Add this field
	peerChainLengths  sync.Map             // Store peer chain lengths
}

// NewNetwork creates a new network instance
func NewNetwork(port string, peerID string) *Network {
	ctx := context.Background()

	var h host.Host
	var err error

	// Create a new libp2p host
	if peerID != "" {
		// Create a deterministic seed from the peer ID
		seed := sha256.Sum256([]byte(peerID))
		priv, _, er := crypto.GenerateKeyPairWithReader(crypto.Ed25519, -1, bytes.NewReader(seed[:]))
		if er != nil {
			logger.WithError(er).Fatal("Failed to generate key pair")
		}
		h, err = libp2p.New(
			libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%s", port)),
			libp2p.Identity(priv),
		)
	} else {
		h, err = libp2p.New(
			libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%s", port)),
		)
	}

	if err != nil {
		logger.WithError(err).Fatal("Failed to create libp2p host")
	}
	// Get the complete multiaddr including peer ID
	hostAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%s", port))
	completeAddr := hostAddr.Encapsulate(multiaddr.StringCast("/p2p/" + h.ID().String()))

	network := &Network{
		Validators:        make(map[string]*Validator),
		Blockchain:        make([]*Block, 0),
		Proposals:         make(map[string]*BlockProposal),
		Port:              port,
		Address:           completeAddr.String(),
		MyValidator:       nil, // Will be set in CreateValidator
		Peers:             make(map[string]*Validator),
		SeenMessages:      make(map[string]time.Time),
		lastAnnouncements: make(map[string]time.Time), // Initialize the new field
		host:              h,
		ctx:               ctx,
		peerChainLengths:  sync.Map{},
	}

	// Set up stream handler for incoming messages
	h.SetStreamHandler(protocol.ID("/pos-consensus/1.0.0"), network.handleStream)

	return network
}

// handleStream handles incoming libp2p streams
func (n *Network) handleStream(stream network.Stream) {
	defer stream.Close()

	// Read the message
	buf := make([]byte, 1024*1024) // 1MB buffer
	bytesRead, err := stream.Read(buf)
	if err != nil {
		logger.WithError(err).Error("Error reading from stream")
		return
	}

	// Unmarshal the message
	var msg GossipMessage
	if err := json.Unmarshal(buf[:bytesRead], &msg); err != nil {
		logger.WithError(err).Error("Error unmarshaling message")
		return
	}

	// Process the message
	go n.processGossipMessage(&msg)
}

// generateKeyPair generates a simple key pair (simplified for demo)
func generateKeyPair() (string, string) {
	// Generate a random private key
	privateKey := make([]byte, 32)
	rand.Read(privateKey)

	// Simple public key derivation (in real implementation, use proper crypto)
	hasher := sha256.New()
	hasher.Write(privateKey)
	publicKey := hasher.Sum(nil)

	return hex.EncodeToString(privateKey), hex.EncodeToString(publicKey)
}

// calculateHash calculates the hash of a block
func (b *Block) calculateHash() string {
	// Use a fixed format for the timestamp to ensure deterministic hashing
	// RFC3339Nano provides a precise, unambiguous, and consistent format.
	timestampStr := b.Timestamp.Format(time.RFC3339Nano)

	data := fmt.Sprintf("%d%s%s%s%s", b.Index, timestampStr, b.Data, b.PreviousHash, b.Validator)
	hasher := sha256.New()
	hasher.Write([]byte(data))
	return hex.EncodeToString(hasher.Sum(nil))
}

// signMessage creates a simple signature for a message
func (n *Network) signMessage(data string) string {
	signatureData := data + n.MyValidator.PrivateKey
	hasher := sha256.New()
	hasher.Write([]byte(signatureData))
	return hex.EncodeToString(hasher.Sum(nil))
}

// generateMessageID generates a unique message ID
func generateMessageID() string {
	timestamp := time.Now().UnixNano()
	random := make([]byte, 8)
	rand.Read(random)
	return fmt.Sprintf("%d_%s", timestamp, hex.EncodeToString(random)[:8])
}

// CreateValidator creates a new validator
func (n *Network) CreateValidator(stake int64) *Validator {
	privateKey, publicKey := generateKeyPair()

	validator := &Validator{
		ID:         publicKey[:16], // Use first 16 chars of public key as ID
		Address:    n.Address,
		PublicKey:  publicKey,
		PrivateKey: privateKey,
		Stake:      stake,
		IsOnline:   true,
		LastSeen:   time.Now(),
	}

	n.mu.Lock()
	n.Validators[validator.ID] = validator
	n.MyValidator = validator
	n.mu.Unlock()

	return validator
}

// ConnectToPeers connects to a list of peer addresses
func (n *Network) ConnectToPeers(peerAddresses []string) {
	for _, addr := range peerAddresses {
		if addr != n.Address && addr != "" {
			go n.connectToPeer(addr)
		}
	}

	// Initiate chain length request after connecting to peers
	time.Sleep(5 * time.Second) // Give time to connect
	go n.initiateChainLengthRequest()
}

// connectToPeer attempts to connect to a single peer
func (n *Network) connectToPeer(address string) {
	// Convert address to multiaddr
	addr, err := multiaddr.NewMultiaddr(address)
	if err != nil {
		logger.WithError(err).Error("Error parsing multiaddr")
		return
	}

	// Get peer ID from multiaddr
	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		logger.WithError(err).Error("Error getting peer info")
		return
	}

	// Connect to the peer
	if err := n.host.Connect(n.ctx, *peerInfo); err != nil {
		logger.WithError(err).Error("Error connecting to peer")
		return
	}
	logger.WithField("peer_id", peerInfo.ID.String()).WithField("address", address).Info("Successfully connected to peer")

	// Announce ourselves to the peer
	announcement := &Validator{
		ID:        n.MyValidator.ID,
		Address:   n.MyValidator.Address,
		PublicKey: n.MyValidator.PublicKey,
		Stake:     n.MyValidator.Stake,
		IsOnline:  true,
		LastSeen:  time.Now(),
	}

	msg := &GossipMessage{
		Type:      MessageTypePeerAnnounce,
		From:      n.MyValidator.ID,
		Timestamp: time.Now(),
		MessageID: generateMessageID(),
		TTL:       3,
		Payload:   announcement,
	}

	msg.Signature = n.signMessage(msg.MessageID)

	// Send announcement to peer
	n.sendMessageToPeer(address, msg)

	// Wait a bit for the announcement to be processed
	time.Sleep(2 * time.Second)
}

// sendMessageToPeer sends a message to a peer at a given address
func (n *Network) sendMessageToPeer(address string, msg *GossipMessage) {
	addr, err := multiaddr.NewMultiaddr(address)
	if err != nil {
		logger.WithError(err).Error("Error parsing multiaddr")
		return
	}
	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		logger.WithError(err).Error("Error getting peer info")
		return
	}

	stream, err := n.host.NewStream(n.ctx, peerInfo.ID, protocol.ID("/pos-consensus/1.0.0"))
	if err != nil {
		logger.WithError(err).Error("Error opening stream to peer")
		return
	}
	defer stream.Close()

	jsonData, err := json.Marshal(msg)
	if err != nil {
		logger.WithError(err).Error("Error marshaling message")
		return
	}

	_, err = stream.Write(jsonData)
	if err != nil {
		logger.WithError(err).Error("Error writing to stream to peer")
		return
	}
}

// processGossipMessage processes an incoming gossip message
func (n *Network) processGossipMessage(msg *GossipMessage) {
	// Check if we've already seen this message
	n.messageMu.Lock()
	if _, seen := n.SeenMessages[msg.MessageID]; seen {
		n.messageMu.Unlock()
		return
	}
	n.SeenMessages[msg.MessageID] = time.Now()
	n.messageMu.Unlock()

	// Don't process our own messages
	if msg.From == n.MyValidator.ID {
		return
	}

	// Process based on message type
	switch msg.Type {
	case MessageTypePeerAnnounce:
		n.handlePeerAnnouncement(msg)
	case MessageTypeBlockProposal:
		n.handleNetworkBlockProposal(msg)
	case MessageTypeVote:
		n.handleNetworkVote(msg)
	case MessageTypeHeartbeat:
		n.handleHeartbeat(msg)
	case RequestChainLength:
		n.handleRequestChainLength(msg)
	case RespondChainLength:
		n.handleRespondChainLength(msg)
	case RequestBlockchain:
		n.handleRequestBlockchain(msg)
	case RespondBlockchain:
		n.handleRespondBlockchain(msg)
	}

	// Gossip the message to other peers (if TTL > 0)
	if msg.TTL > 0 {
		msg.TTL--
		n.gossipMessage(msg)
	}
}

// initiateChainLengthRequest asks connected peers for their chain length
func (n *Network) initiateChainLengthRequest() {
	n.mu.RLock()
	defer n.mu.RUnlock()

	for peerID, peer := range n.Peers {
		if peer.IsOnline {
			msg := &GossipMessage{
				Type:      RequestChainLength,
				From:      n.MyValidator.ID,
				Timestamp: time.Now(),
				MessageID: generateMessageID(),
				TTL:       1,
				Payload:   nil,
			}
			n.sendMessageToPeer(peer.Address, msg)
			logger.WithField("peer_id", peerID[:16]).Info("Sent chain length request")
		}
	}
}

// handleRequestChainLength responds to a request for our chain length
func (n *Network) handleRequestChainLength(msg *GossipMessage) {
	length := int64(len(n.Blockchain))
	respMsg := &GossipMessage{
		Type:      RespondChainLength,
		From:      n.MyValidator.ID,
		To:        msg.From,
		Timestamp: time.Now(),
		MessageID: generateMessageID(),
		TTL:       0,
		Payload:   length,
	}
	// Find the peer's address to send the response
	n.mu.RLock()
	if peer, exists := n.Peers[msg.From]; exists {
		n.mu.RUnlock()
		n.sendMessageToPeer(peer.Address, respMsg)
		logger.WithField("peer_id", msg.From[:16]).WithField("chain_length", length).Info("Sent chain length")
	} else {
		n.mu.RUnlock()
		logger.WithField("peer_id", msg.From[:16]).Error("Could not find peer to respond with chain length")
	}
}

// handleRespondChainLength processes a peer's chain length response
func (n *Network) handleRespondChainLength(msg *GossipMessage) {
	peerID := msg.From
	lengthFloat, ok := msg.Payload.(float64)
	if !ok {
		logger.WithField("peer_id", peerID[:16]).WithField("payload", msg.Payload).Error("Invalid chain length response")
		return
	}
	length := int64(lengthFloat)
	n.peerChainLengths.Store(peerID, length)
	logger.WithField("peer_id", peerID[:16]).WithField("chain_length", length).Info("Received chain length")
	n.checkForLongestChain()
}

// checkForLongestChain checks if any peer has a longer chain and requests it
func (n *Network) checkForLongestChain() {
	longestLength := int64(len(n.Blockchain))
	var longestPeerID string

	n.peerChainLengths.Range(func(key, value interface{}) bool {
		peerID := key.(string)
		length := value.(int64)
		if length > longestLength {
			longestLength = length
			longestPeerID = peerID
		}
		return true
	})

	if longestPeerID != "" {
		logger.WithField("peer_id", longestPeerID[:16]).WithField("chain_length", longestLength).WithField("local_chain_length", len(n.Blockchain)).Info("Peer has a longer chain. Requesting blockchain")
		n.requestBlockchain(longestPeerID)
	}
}

// requestBlockchain requests the full blockchain from a peer
func (n *Network) requestBlockchain(peerID string) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if peer, exists := n.Peers[peerID]; exists && peer.IsOnline {
		msg := &GossipMessage{
			Type:      RequestBlockchain,
			From:      n.MyValidator.ID,
			To:        peerID,
			Timestamp: time.Now(),
			MessageID: generateMessageID(),
			TTL:       0,
			Payload:   nil,
		}
		n.sendMessageToPeer(peer.Address, msg)
		logger.WithField("peer_id", peerID[:16]).Info("Requested blockchain")
	} else {
		logger.WithField("peer_id", peerID[:16]).Error("Could not find online peer to request blockchain from")
	}
}

// handleRequestBlockchain responds to a request for the full blockchain
func (n *Network) handleRequestBlockchain(msg *GossipMessage) {
	respMsg := &GossipMessage{
		Type:      RespondBlockchain,
		From:      n.MyValidator.ID,
		To:        msg.From,
		Timestamp: time.Now(),
		MessageID: generateMessageID(),
		TTL:       0,
		Payload:   n.Blockchain,
	}
	// Find the peer's address
	n.mu.RLock()
	if peer, exists := n.Peers[msg.From]; exists {
		n.mu.RUnlock()
		n.sendMessageToPeer(peer.Address, respMsg)
		logger.WithField("peer_id", msg.From[:16]).WithField("blockchain_length", len(n.Blockchain)).Info("Sent blockchain")
	} else {
		n.mu.RUnlock()
		logger.WithField("peer_id", msg.From[:16]).Error("Could not find peer to send blockchain")
	}
}

// handleRespondBlockchain processes a received blockchain
func (n *Network) handleRespondBlockchain(msg *GossipMessage) {
	peerID := msg.From
	receivedChainData, ok := msg.Payload.([]interface{})
	if !ok {
		logger.WithField("peer_id", peerID[:16]).WithField("payload", msg.Payload).Error("Invalid blockchain response")
		return
	}

	var receivedChain []*Block
	for _, blockData := range receivedChainData {
		blockJSON, err := json.Marshal(blockData)
		if err != nil {
			logger.WithField("peer_id", peerID[:16]).WithError(err).Error("Error marshaling block data")
			return
		}
		var block Block
		if err := json.Unmarshal(blockJSON, &block); err != nil {
			logger.WithField("peer_id", peerID[:16]).WithError(err).Error("Error unmarshaling block")
			return
		}
		receivedChain = append(receivedChain, &block)
	}

	logger.WithField("peer_id", peerID[:16]).WithField("blockchain_length", len(receivedChain)).Info("Received blockchain, validating")
	if n.isChainValid(receivedChain) && int64(len(receivedChain)) > int64(len(n.Blockchain)) {
		logger.WithField("peer_id", peerID[:16]).Info("Received valid and longer chain. Replacing local blockchain")
		n.mu.Lock()
		n.Blockchain = receivedChain
		n.mu.Unlock()
		// Optionally, trigger a new consensus round or other actions after sync
	} else {
		logger.WithField("peer_id", peerID[:16]).Error("Received invalid or shorter blockchain")
	}
}

// isChainValid checks if a given blockchain is valid
func (n *Network) isChainValid(chain []*Block) bool {
	if len(chain) <= 1 {
		return true
	}
	for i := 1; i < len(chain); i++ {
		currentBlock := chain[i]
		previousBlock := chain[i-1]
		if currentBlock.PreviousHash != previousBlock.Hash {
			logger.WithField("block_index", currentBlock.Index).WithField("expected_hash", previousBlock.Hash[:16]).WithField("got_hash", currentBlock.PreviousHash[:16]).Error("Invalid previous hash")
			return false
		}
		if currentBlock.Hash != currentBlock.calculateHash() {
			logger.WithField("block_index", currentBlock.Index).WithField("expected_hash", currentBlock.calculateHash()[:16]).WithField("got_hash", currentBlock.Hash[:16]).Error("Invalid hash")
			return false
		}
	}
	return true
}

// handlePeerAnnouncement handles peer announcement messages
func (n *Network) handlePeerAnnouncement(msg *GossipMessage) {
	payloadBytes, _ := json.Marshal(msg.Payload)
	var validator Validator
	if err := json.Unmarshal(payloadBytes, &validator); err != nil {
		logger.WithError(err).Error("Error unmarshaling peer announcement")
		return
	}

	// Check if we've announced to this peer recently
	n.mu.Lock()
	lastAnnounce, exists := n.lastAnnouncements[validator.ID]
	if exists && time.Since(lastAnnounce) < AnnouncementCooldown {
		n.mu.Unlock()
		return // Skip if we've announced recently
	}
	n.lastAnnouncements[validator.ID] = time.Now()
	n.mu.Unlock()

	logger.WithField("validator_id", validator.ID).WithField("stake", validator.Stake).Info("Received peer announcement")

	// Add the peer if we don't know them
	n.mu.Lock()
	if existingValidator, exists := n.Validators[validator.ID]; exists {
		// Update existing validator's information
		existingValidator.Address = validator.Address
		existingValidator.PublicKey = validator.PublicKey
		existingValidator.Stake = validator.Stake
		existingValidator.IsOnline = true
		existingValidator.LastSeen = time.Now()
	} else {
		// Add new validator
		n.Validators[validator.ID] = &validator
	}

	// Always update in peers map
	n.Peers[validator.ID] = &validator
	n.mu.Unlock()

	// Send our own announcement back only if we haven't announced recently
	if validator.ID != n.MyValidator.ID {
		announcement := &Validator{
			ID:        n.MyValidator.ID,
			Address:   n.MyValidator.Address,
			PublicKey: n.MyValidator.PublicKey,
			Stake:     n.MyValidator.Stake,
			IsOnline:  true,
			LastSeen:  time.Now(),
		}

		msg := &GossipMessage{
			Type:      MessageTypePeerAnnounce,
			From:      n.MyValidator.ID,
			To:        validator.ID,
			Timestamp: time.Now(),
			MessageID: generateMessageID(),
			TTL:       1, // Reduce TTL to prevent excessive propagation
			Payload:   announcement,
		}
		msg.Signature = n.signMessage(msg.MessageID)
		n.sendMessageToPeer(validator.Address, msg)
	}
}

// handleNetworkBlockProposal handles block proposal messages from network
func (n *Network) handleNetworkBlockProposal(msg *GossipMessage) {
	payloadBytes, _ := json.Marshal(msg.Payload)
	var block Block
	if err := json.Unmarshal(payloadBytes, &block); err != nil {
		logger.WithError(err).Error("Error unmarshaling block proposal")
		return
	}

	logger.WithField("block_index", block.Index).WithField("from", msg.From).Info("Received block proposal")

	isValid := n.ValidateBlock(&block) // Always use ValidateBlock

	logger.WithField("block_index", block.Index).WithField("from", msg.From).WithField("valid", isValid).Info("Block validation result")

	// Create and send vote
	vote := &Vote{
		ProposalHash: block.Hash,
		ValidatorID:  n.MyValidator.ID,
		Approve:      isValid,
		Timestamp:    time.Now(),
	}
	vote.Signature = n.signMessage(vote.ProposalHash + vote.ValidatorID)

	// Send vote back to network
	voteMsg := &GossipMessage{
		Type:      MessageTypeVote,
		From:      n.MyValidator.ID,
		Timestamp: time.Now(),
		MessageID: generateMessageID(),
		TTL:       2,
		Payload:   vote,
	}
	voteMsg.Signature = n.signMessage(voteMsg.MessageID)
	n.gossipMessage(voteMsg)

	n.mu.Lock()
	if _, exists := n.Proposals[block.Hash]; !exists {
		n.Proposals[block.Hash] = &BlockProposal{
			Block:      block,
			Proposer:   msg.From,
			Votes:      make(map[string]bool),
			VoteCount:  0,
			TotalStake: n.getTotalStakeUnsafe(),
			VotedStake: 0,
			Timestamp:  time.Now(),
		}
	}

	if isValid {
		n.Proposals[block.Hash].Votes[n.MyValidator.ID] = true
		validator, exists := n.Validators[n.MyValidator.ID]
		if exists {
			n.Proposals[block.Hash].VotedStake += validator.Stake
		}
	}
	n.mu.Unlock()

	logger.WithField("validator_id", n.MyValidator.ID).WithField("valid", isValid).WithField("block_hash", block.Hash[:16]).Info("Vote cast")

	// Immediately check consensus after casting our vote
	n.checkConsensus(block.Hash)
}

// handleNetworkVote handles vote messages from network
func (n *Network) handleNetworkVote(msg *GossipMessage) {
	payloadBytes, _ := json.Marshal(msg.Payload)
	var vote Vote
	if err := json.Unmarshal(payloadBytes, &vote); err != nil {
		logger.WithError(err).Error("Error unmarshaling vote")
		return
	}

	logger.WithField("from", msg.From).WithField("valid", vote.Approve).WithField("proposal_hash", vote.ProposalHash[:16]).Info("Received vote")

	n.mu.Lock()
	proposal, exists := n.Proposals[vote.ProposalHash]
	if !exists {
		n.mu.Unlock()
		return
	}

	// Record the vote
	if _, alreadyVoted := proposal.Votes[vote.ValidatorID]; !alreadyVoted {
		proposal.Votes[vote.ValidatorID] = vote.Approve
		if vote.Approve {
			proposal.VoteCount++
			if validator, exists := n.Validators[vote.ValidatorID]; exists && validator.IsOnline {
				proposal.VotedStake += validator.Stake
			}
		}
		// Update total stake in case validators have changed
		proposal.TotalStake = n.getTotalStakeUnsafe()
		logger.WithField("voted_stake", proposal.VotedStake).WithField("total_stake", proposal.TotalStake).Info("Updated stake")
	}
	n.mu.Unlock()

	// Check consensus
	n.checkConsensus(vote.ProposalHash)
}

// handleHeartbeat handles heartbeat messages
func (n *Network) handleHeartbeat(msg *GossipMessage) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if validator, exists := n.Validators[msg.From]; exists {
		// Update the validator's state directly
		validator.LastSeen = time.Now()
		validator.IsOnline = true

		// Also update in peers map if it exists there
		if peer, exists := n.Peers[msg.From]; exists {
			peer.LastSeen = validator.LastSeen
			peer.IsOnline = validator.IsOnline
		}
	}
}

// gossipMessage broadcasts a message to all connected peers
func (n *Network) gossipMessage(msg *GossipMessage) {
	n.mu.RLock()
	peers := make([]*Validator, 0, len(n.Peers))
	for _, peer := range n.Peers {
		if peer.ID != n.MyValidator.ID && peer.IsOnline {
			peers = append(peers, peer)
		}
	}
	n.mu.RUnlock()

	// Send to all peers concurrently
	for _, peer := range peers {
		go n.sendMessageToPeer(peer.Address, msg)
	}
}

// SelectLeader selects a leader based on stake-weighted random selection
func (n *Network) SelectLeader() string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if len(n.Validators) == 0 {
		return ""
	}

	// Calculate total stake of online validators
	totalStake := int64(0)
	onlineValidators := make([]*Validator, 0)

	for _, validator := range n.Validators {
		if validator.IsOnline {
			totalStake += validator.Stake
			onlineValidators = append(onlineValidators, validator)
		}
	}

	if totalStake == 0 {
		return ""
	}

	// Add time-based entropy
	seed := time.Now().UnixNano()
	random, _ := rand.Int(rand.Reader, big.NewInt(totalStake+seed%1000))
	randomValue := random.Int64()

	// Select validator based on stake weight
	currentWeight := int64(0)
	for _, validator := range onlineValidators {
		currentWeight += validator.Stake
		if randomValue <= currentWeight {
			return validator.ID
		}
	}

	// Fallback to first validator
	if len(onlineValidators) > 0 {
		return onlineValidators[0].ID
	}

	return ""
}

// ProposeBlock creates and proposes a new block
func (n *Network) ProposeBlock(data string) (*Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.MyValidator == nil {
		return nil, fmt.Errorf("no validator configured for this node")
	}

	// Get the last block
	var previousHash string
	var index int64

	if len(n.Blockchain) == 0 {
		// For the first block, use genesis hash
		previousHash = strings.Repeat("0", 64)
		index = 0
	} else {
		lastBlock := n.Blockchain[len(n.Blockchain)-1]
		previousHash = lastBlock.Hash
		index = lastBlock.Index + 1
	}

	logger.WithField("block_index", index).WithField("previous_hash", previousHash).Info("Proposing new block")

	// Create new block
	block := &Block{
		Index:        index,
		Timestamp:    time.Now(),
		Data:         data,
		PreviousHash: previousHash,
		Validator:    n.MyValidator.ID,
	}

	block.Hash = block.calculateHash()
	block.Signature = n.signMessage(block.Hash)

	return block, nil
}

// ValidateBlock validates a proposed block
func (n *Network) ValidateBlock(block *Block) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()

	// Basic validation checks
	if block == nil {
		logger.Error("Block validation failed: block is nil")
		return false
	}

	// For the first block (index 0), we need special handling
	if len(n.Blockchain) == 0 && block.Index == 0 {
		logger.WithField("block_hash", block.Hash[:16]).WithField("calculated_hash", block.calculateHash()[:16]).WithField("previous_hash", block.PreviousHash[:16]).Info("Validating first block")
		if block.calculateHash() != block.Hash {
			logger.Error("First block validation failed: invalid hash")
			return false
		}
		if block.PreviousHash != strings.Repeat("0", 64) {
			logger.Error("First block validation failed: invalid previous hash")
			return false
		}
		logger.Info("First block validation: accepting first valid block")
		return true
	}

	// Check if validator exists and is online
	validator, exists := n.Validators[block.Validator]
	if !exists {
		// If validator not found, try to discover them
		logger.WithField("validator_id", block.Validator).Error("Validator not found, attempting discovery")
		return false
	}
	if !validator.IsOnline {
		logger.WithField("validator_id", block.Validator).Error("Block validation failed: validator is offline")
		return false
	}

	// Check index continuity
	if len(n.Blockchain) > 0 {
		lastBlock := n.Blockchain[len(n.Blockchain)-1]
		if block.Index != lastBlock.Index+1 {
			logger.WithField("expected_index", lastBlock.Index+1).WithField("got_index", block.Index).Error("Block validation failed: invalid index")
			return false
		}
		if block.PreviousHash != lastBlock.Hash {
			logger.Error("Block validation failed: invalid previous hash")
			return false
		}
	} else {
		if block.Index != 0 {
			logger.Error("Block validation failed: first block must have index 0")
			return false
		}
	}

	// Validate hash
	expectedHash := block.calculateHash()
	if block.Hash != expectedHash {
		logger.WithField("expected_hash", expectedHash[:16]).WithField("got_hash", block.Hash[:16]).Error("Block validation failed: invalid hash")
		return false
	}

	// Validate timestamp (not too far in future)
	if block.Timestamp.After(time.Now().Add(10 * time.Minute)) {
		logger.Error("Block validation failed: timestamp too far in future")
		return false
	}

	logger.WithField("block_index", block.Index).Info("Block validation successful")
	return true
}

// StartConsensusRound starts a new consensus round
func (n *Network) StartConsensusRound() {
	n.mu.Lock()
	// For single node case, we can proceed immediately
	if len(n.Validators) == 1 {
		n.CurrentLeader = n.MyValidator.ID
		n.mu.Unlock()
		logger.WithField("leader_id", n.MyValidator.ID).Info("Single node mode: I am the leader")
		n.proposeBlockAsLeader()
		return
	}

	// For multiple nodes, we need to synchronize leader selection
	// Use a deterministic seed based on current block height and last block timestamp
	blockHeight := int64(len(n.Blockchain))
	var timeSlot int64
	if len(n.Blockchain) > 0 {
		// Use the last block's timestamp for deterministic time slots
		timeSlot = n.Blockchain[len(n.Blockchain)-1].Timestamp.Unix() / 15
	} else {
		// For genesis block, use current time but round to nearest 15 seconds
		timeSlot = time.Now().Unix() / 15
	}
	seed := timeSlot + blockHeight

	// Calculate total stake of online validators
	totalStake := int64(0)
	onlineValidators := make([]*Validator, 0)
	for _, validator := range n.Validators {
		if validator.IsOnline {
			totalStake += validator.Stake
			onlineValidators = append(onlineValidators, validator)
		}
	}

	if totalStake == 0 {
		n.mu.Unlock()
		logger.Error("No online validators available")
		return
	}

	// Sort validators by ID to ensure consistent ordering
	sort.Slice(onlineValidators, func(i, j int) bool {
		return onlineValidators[i].ID < onlineValidators[j].ID
	})

	// Use a deterministic hash of the seed for selection
	hasher := sha256.New()
	hasher.Write([]byte(fmt.Sprintf("%d", seed)))
	hashBytes := hasher.Sum(nil)
	hashInt := new(big.Int).SetBytes(hashBytes)
	randomValue := hashInt.Mod(hashInt, big.NewInt(totalStake)).Int64()

	// Select leader based on stake weight and deterministic seed
	currentWeight := int64(0)
	var leaderID string
	for _, validator := range onlineValidators {
		currentWeight += validator.Stake
		if randomValue <= currentWeight {
			leaderID = validator.ID
			break
		}
	}

	if leaderID == "" && len(onlineValidators) > 0 {
		leaderID = onlineValidators[0].ID
	}

	n.CurrentLeader = leaderID
	n.mu.Unlock()

	logger.WithField("leader_id", leaderID).WithField("seed", seed).WithField("block_height", blockHeight).Info("Leader selected for this round")

	// If this node is the leader, propose a block
	if leaderID == n.MyValidator.ID {
		n.proposeBlockAsLeader()
	}
}

// proposeBlockAsLeader handles block proposal when this node is the leader
func (n *Network) proposeBlockAsLeader() {
	logger.Info("I am the leader! Proposing a new block...")

	blockData := fmt.Sprintf("Block_%d_by_%s", len(n.Blockchain), n.MyValidator.ID)
	block, err := n.ProposeBlock(blockData)
	if err != nil {
		logger.WithError(err).Error("Error proposing block")
		return
	}

	// Broadcast block proposal to network
	n.broadcastBlockProposal(block)
}

// broadcastBlockProposal broadcasts a block proposal to the network
func (n *Network) broadcastBlockProposal(block *Block) {
	logger.WithField("block_index", block.Index).WithField("hash", block.Hash[:16]).Info("Broadcasting block proposal")

	// Create proposal tracking
	n.mu.Lock()
	n.Proposals[block.Hash] = &BlockProposal{
		Block:      *block,
		Proposer:   n.MyValidator.ID,
		Votes:      make(map[string]bool),
		VoteCount:  0,
		TotalStake: n.getTotalStakeUnsafe(),
		VotedStake: 0,
		Timestamp:  time.Now(),
	}
	// Auto-vote for our own proposal
	n.Proposals[block.Hash].Votes[n.MyValidator.ID] = true
	n.Proposals[block.Hash].VoteCount = 1
	n.Proposals[block.Hash].VotedStake = n.MyValidator.Stake
	n.mu.Unlock()

	// Create gossip message
	msg := &GossipMessage{
		Type:      MessageTypeBlockProposal,
		From:      n.MyValidator.ID,
		Timestamp: time.Now(),
		MessageID: generateMessageID(),
		TTL:       3,
		Payload:   block,
	}
	msg.Signature = n.signMessage(msg.MessageID)

	// Broadcast to network
	n.gossipMessage(msg)

	logger.WithField("validator_id", n.MyValidator.ID).Info("Vote cast by: true (Total votes: 1)")

	// Immediately check consensus after auto-vote (important for single-node case)
	n.checkConsensus(block.Hash)
}

// checkConsensus checks if consensus has been reached for a proposal
func (n *Network) checkConsensus(proposalHash string) {
	n.mu.Lock()
	proposal, exists := n.Proposals[proposalHash]
	if !exists {
		n.mu.Unlock()
		return
	}

	// Update total stake in case validators have changed
	proposal.TotalStake = n.getTotalStakeUnsafe()

	// Consensus rule: Need >50% of stake to approve
	requiredStake := proposal.TotalStake/2 + 1

	logger.WithField("voted_stake", proposal.VotedStake).WithField("total_stake", proposal.TotalStake).WithField("required_stake", requiredStake).Info("Checking consensus")

	if proposal.VotedStake >= requiredStake {
		logger.WithField("block_index", proposal.Block.Index).WithField("voted_stake", proposal.VotedStake).WithField("total_stake", proposal.TotalStake).Info("Consensus reached! Block accepted")

		// Create a new block instance to avoid pass by value issues
		newBlock := &Block{
			Index:        proposal.Block.Index,
			Timestamp:    proposal.Block.Timestamp,
			Data:         proposal.Block.Data,
			PreviousHash: proposal.Block.PreviousHash,
			Hash:         proposal.Block.Hash,
			Validator:    proposal.Block.Validator,
			Signature:    proposal.Block.Signature,
		}

		// Add block to blockchain
		n.Blockchain = append(n.Blockchain, newBlock)

		// Clean up proposal
		delete(n.Proposals, proposalHash)
		chainLength := len(n.Blockchain)
		n.mu.Unlock()

		logger.WithField("chain_length", chainLength).Info("Block added to chain")

		// Broadcast the accepted block to ensure all nodes have the same chain
		blockMsg := &GossipMessage{
			Type:      MessageTypeBlockProposal,
			From:      n.MyValidator.ID,
			Timestamp: time.Now(),
			MessageID: generateMessageID(),
			TTL:       3,
			Payload:   newBlock,
		}
		blockMsg.Signature = n.signMessage(blockMsg.MessageID)
		n.gossipMessage(blockMsg)

		// Wait a bit to ensure the block is processed by all nodes
		time.Sleep(2 * time.Second)

		// If this was the first block, wait longer and broadcast again
		if newBlock.Index == 0 {
			time.Sleep(3 * time.Second)
			// Broadcast again to ensure all nodes have it
			blockMsg.MessageID = generateMessageID()
			blockMsg.Signature = n.signMessage(blockMsg.MessageID)
			n.gossipMessage(blockMsg)
		}
	} else if len(proposal.Votes) >= len(n.Validators) {
		// All validators have voted but consensus not reached
		logger.WithField("voted_stake", proposal.VotedStake).WithField("total_stake", proposal.TotalStake).WithField("required_stake", requiredStake).Error("Consensus failed! Block rejected")
		delete(n.Proposals, proposalHash)
		n.mu.Unlock()
	} else {
		n.mu.Unlock()
	}
}

// getTotalStakeUnsafe calculates total stake without acquiring locks (caller must hold lock)
func (n *Network) getTotalStakeUnsafe() int64 {
	total := int64(0)
	for _, validator := range n.Validators {
		if validator.IsOnline {
			total += validator.Stake
		}
	}
	return total
}

// startHeartbeat starts sending periodic heartbeat messages
func (n *Network) startHeartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for range ticker.C {
			msg := &GossipMessage{
				Type:      MessageTypeHeartbeat,
				From:      n.MyValidator.ID,
				Timestamp: time.Now(),
				MessageID: generateMessageID(),
				TTL:       1,
				Payload:   nil,
			}
			msg.Signature = n.signMessage(msg.MessageID)
			n.gossipMessage(msg)
		}
	}()
}

// cleanupOldMessages periodically cleans up old seen messages
func (n *Network) cleanupOldMessages() {
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		for range ticker.C {
			n.messageMu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for msgID, timestamp := range n.SeenMessages {
				if timestamp.Before(cutoff) {
					delete(n.SeenMessages, msgID)
				}
			}
			n.messageMu.Unlock()
		}
	}()
}

func main() {
	port := flag.String("port", "8000", "The port to listen on")
	peers := flag.String("peers", "", "Comma-separated list of peer addresses to connect to")
	peerID := flag.String("peerid", "", "Unique ID for this peer (optional, for deterministic keys)")
	stake := flag.Int64("stake", 100, "Initial stake of this validator")
	flag.Parse()

	logger = logrus.New()

	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
		DisableColors: false,
	})
	logger.SetLevel(logrus.InfoLevel)

	network := NewNetwork(*port, *peerID)
	network.CreateValidator(*stake)

	logger.WithField("validator_id", network.MyValidator.ID[:16]).WithField("address", network.Address).WithField("port", *port).Info("PoS Network Node started")

	if *peers != "" {
		peerList := strings.Split(*peers, ",")
		logger.WithField("peers", peerList).Info("Connecting to peers")
		network.ConnectToPeers(peerList)
	}

	// Start background tasks
	go func() {
		time.Sleep(10 * time.Second) // Wait for peers to connect
		for {
			network.StartConsensusRound()
			time.Sleep(15 * time.Second) // Interval between consensus rounds
		}
	}()
	network.startHeartbeat()
	network.cleanupOldMessages()

	// Keep the node running
	select {}
}
