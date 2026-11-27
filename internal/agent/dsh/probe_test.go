package dsh

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

func TestProbeRequestBudgetAllowsRetriesAndRetainsToolLimit(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the probe plugin contract")
	}
	opts := Options{StateDir: t.TempDir(), Model: ModelConfig{Protocol: "openai-completions", ModelID: "probe-model"}, probe: &probeOptions{receipt: "fixed-receipt"}}
	patch, err := json.Marshal(profile(opts))
	if err != nil {
		t.Fatal(err)
	}
	const script = `
import assert from 'node:assert/strict';
import {apply} from './probe_tool.mjs';
const patches = JSON.parse(process.argv[1]);
const config = patches.flatMap(p => p.insert || []).find(p => p.id === 'teamx-probe').config;
const retries = patches.find(p => p.id === 'llm-pi-ai').config.providers.teamx.retryPolicy.maxRetries;
const events = new Map(); let tool;
apply({on:(name,fn)=>events.set(name,fn),provide:()=>{}}, config);
const agent = {ctx:{tools:{restrict:({allow})=>assert.deepEqual(allow,[]),register:value=>{tool=value;},view:()=>({visible:new Map([['teamx_probe',{}]])})}}};
events.get('agent/created')({agent});
let requests = 0;
const request = () => events.get('agent/request')({agent},()=>{requests++;});
// Each of the three required model requests may consume all provider retries.
for (let step=0; step<3; step++) for (let attempt=0; attempt<=retries; attempt++) request();
assert.equal(await tool.execute(), 'fixed-receipt');
await assert.rejects(tool.execute(), /probe tool budget exceeded/);
while (requests < config.maxRequests) request();
assert.throws(request, /probe request budget exceeded/);
assert.equal(requests, 4 * (retries + 1));
`
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--input-type=module", "-e", script, string(patch))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("probe contract: %v\n%s", err, output)
	}
}
