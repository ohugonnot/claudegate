#!/bin/bash
# Mock Claude CLI for testing
# Outputs stream-json format

# Parse args to find the prompt (last argument)
PROMPT="${@: -1}"

# Simulate processing delay
sleep 0.1

# Output stream-json format
echo '{"type":"assistant","content":[{"type":"text","text":"Hello from mock Claude!"}]}'
echo '{"type":"result","result":"Hello from mock Claude!","model":"haiku","stop_reason":"end_turn"}'
