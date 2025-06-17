#!/bin/bash

# Script to run multiple PoS validator instances with P2P gossip

echo "Starting PoS Consensus Network with P2P Gossip"
echo "================================================="
echo ""

# Function to run validator in background
run_validator() {
    local port=$1
    local stake=$2
    local name=$3
    local peers=$4
    
    echo "Starting $name on port $port with stake $stake"
    if [[ -n "$peers" ]]; then
        echo "  Connecting to peers: $peers"
        ./pos-validator -port="$port" -stake="$stake" -peers="$peers" > "validator_$port.log" 2>&1 &
    else
        ./pos-validator -port="$port" -stake="$stake" > "validator_$port.log" 2>&1 &
    fi
    echo $! > "validator_$port.pid"
    echo "$name started (PID: $(cat validator_$port.pid))"
}

# Clean up function
cleanup() {
    echo ""
    echo "Stopping all validators..."
    for pidfile in validator_*.pid; do
        if [[ -f "$pidfile" ]]; then
            pid=$(cat "$pidfile")
            kill "$pid" 2>/dev/null
            rm "$pidfile"
        fi
    done
    rm -f validator_*.log
    echo "All validators stopped"
    exit 0
}

# Set up signal handler
trap cleanup SIGINT SIGTERM

echo "🔧 Building validator binary..."
go build -o pos-validator

# Start validators with P2P connections
echo ""
echo "Starting P2P network..."

# Start the first validator (bootstrap node)
run_validator 8080 150 "Validator-Alice"
sleep 3

# Start second validator connecting to first
run_validator 8081 200 "Validator-Bob" "/ip4/127.0.0.1/tcp/8080/p2p/$(cat validator_8080.log | grep 'PoS Network Node started' | grep -o 'address=[^ ]*' | cut -d'=' -f2 | cut -d'/' -f7)"
sleep 3  

# Start third validator connecting to network
run_validator 8082 80 "Validator-Charlie" "/ip4/127.0.0.1/tcp/8080/p2p/$(cat validator_8080.log | grep 'PoS Network Node started' | grep -o 'address=[^ ]*' | cut -d'=' -f2 | cut -d'/' -f7),/ip4/127.0.0.1/tcp/8081/p2p/$(cat validator_8081.log | grep 'PoS Network Node started' | grep -o 'address=[^ ]*' | cut -d'=' -f2 | cut -d'/' -f7)"
sleep 3

# Start fourth validator connecting to bootstrap
run_validator 8083 120 "Validator-Diana" "/ip4/127.0.0.1/tcp/8080/p2p/$(cat validator_8080.log | grep 'PoS Network Node started' | grep -o 'address=[^ ]*' | cut -d'=' -f2 | cut -d'/' -f7)"

echo ""
echo "P2P Network Status:"
echo "- 4 validators running on ports 8080-8083"
echo "- Real P2P communication via libp2p"
echo "- Total stake: 550"
echo "- Consensus threshold: >275 stake (50%+ of total)"
echo ""
echo "Validator Details:"
echo "- Alice (8080): 150 stake (27.3%) - Bootstrap node"
echo "- Bob (8081): 200 stake (36.4%) - Connected to Alice"  
echo "- Charlie (8082): 80 stake (14.5%) - Connected to Alice & Bob"
echo "- Diana (8083): 120 stake (21.8%) - Connected to Alice"
echo ""
echo "Consensus rounds will start automatically every 15 seconds"
echo "Watch real P2P messages being exchanged between validators"
echo "Check individual validator logs:"
echo "   tail -f validator_8080.log  # Alice"
echo "   tail -f validator_8081.log  # Bob"
echo "   tail -f validator_8082.log  # Charlie"
echo "   tail -f validator_8083.log  # Diana"
echo ""
echo "Press Ctrl+C to stop all validators..."

# Wait for user to stop
wait