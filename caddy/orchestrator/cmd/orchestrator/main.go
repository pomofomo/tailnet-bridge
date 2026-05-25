// Command orchestrator is the bridge's PID-1 process. It owns the lifecycle
// described in SPEC §9: parse config, spawn Caddy, poll each community
// directory over an ephemeral tsnet node, push merged Caddy JSON to the
// admin API, run the error/status server, propagate signals.
//
// Implementation lives in the internal packages; this file only wires
// dependencies and runs the top-level loop.
package main

func main() {
	// TODO: see SPEC §9.1.
	//  1. config.Load(os.Getenv("BRIDGE_CONFIG"))
	//  2. caddyproc.Start(cfg, bootstrapPath) — pipes stdout/stderr with prefix
	//  3. status.NewServer(cfg.OrchestratorErrorPort, healthStore)
	//  4. poller.Run(ctx, deps{config, healthStore, adminClient})
	//  5. signal.Notify SIGTERM/SIGINT/SIGHUP — SIGHUP fans out to poller;
	//     SIGTERM/SIGINT forwards to caddyproc and waits up to 30s.
}
