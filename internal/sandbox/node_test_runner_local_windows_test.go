//go:build windows

package sandbox

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixture is a synthetic Node test, never an execution of the user's
// repository on the host. Each command uses the production network-disabled
// LPAC backend with a separate owned copy of the supplied Node distribution.
func TestWindowsLocalNodeTestRunnerWithIsolatedChildren(t *testing.T) {
	// This opt-in is separate from the stock npm loader fixture. A compatible
	// distribution must include libuv's AppContainer LOCAL pipe namespace fix.
	source := os.Getenv("TRAVERSE_BOARD_NODE_LPAC_TEST_RUNNER_FIXTURE")
	if source == "" {
		t.Skip("set TRAVERSE_BOARD_NODE_LPAC_TEST_RUNNER_FIXTURE to a compatible Node distribution for real LPAC test-runner validation")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		t.Fatal(err)
	}
	base := windowsTestTempDir(t)
	toolchain, drydock := filepath.Join(base, "node"), filepath.Join(base, "drydock")
	if err := os.CopyFS(toolchain, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(drydock, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drydock, "probe.test.cjs"), []byte(`const test = require('node:test');
const assert = require('node:assert/strict');
console.log('fixture-file-loaded');
test('isolated-node-test', () => assert.equal(2 + 2, 4));
`), 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(filepath.Join(base, "owners")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	readiness, err := backend.Readiness(t.Context(), LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !readiness.Ready {
		t.Fatalf("Local not ready: %+v %v", readiness, err)
	}
	for _, entry := range []struct {
		name      string
		arguments []string
		marker    string
	}{
		{"eval", []string{"-e", "console.log('isolated-node-eval')"}, "isolated-node-eval"},
		{"explicit-relative", []string{"--test", "--test-reporter=tap", "probe.test.cjs"}, "isolated-node-test"},
		{"discovery", []string{"--test", "--test-reporter=tap"}, "isolated-node-test"},
		{"spawn-inherit", []string{"-e", `console.log('before-spawn');const c=require('node:child_process').spawn(process.execPath,['-e',"console.log('isolated-child-output')"],{stdio:'inherit'});console.log('after-spawn');c.on('error',e=>{console.error(e);process.exitCode=1});c.on('close',code=>{console.log('child-closed='+code);process.exitCode=code})`}, "isolated-child-output"},
		{"spawn-ignore", []string{"-e", `console.log('before-spawn');const c=require('node:child_process').spawn(process.execPath,['-e',"process.exit(0)"],{stdio:'ignore'});console.log('after-spawn');c.on('error',e=>{console.error(e);process.exitCode=1});c.on('close',code=>{console.log('child-closed='+code);process.exitCode=code})`}, "child-closed=0"},
		{"filesystem-relative", []string{"-e", `const fs=require('node:fs');console.log('cwd='+process.cwd());console.log('listing='+JSON.stringify(fs.readdirSync('.')));console.log('read='+fs.readFileSync('probe.test.cjs','utf8').includes('isolated-node-test'));console.log('glob='+JSON.stringify(fs.globSync('*.test.cjs')))`}, "probe.test.cjs"},
		{"spawn-pipes", []string{"-e", `console.log('before-spawn');const c=require('node:child_process').spawn(process.execPath,['-e',"console.log('isolated-child-output')"]);console.log('after-spawn');c.on('spawn',()=>console.log('child-spawned'));c.on('error',e=>{console.error(e);process.exitCode=1});c.stdout.pipe(process.stdout);c.stderr.pipe(process.stderr);c.on('close',code=>{console.log('child-closed='+code);process.exitCode=code})`}, "isolated-child-output"},
	} {
		t.Run(entry.name, func(t *testing.T) {
			request := localWindowsTestRequest(t, backend, drydock, toolchain, "/node", "/node/node.exe", entry.arguments)
			request.Manifest.Resources.MemoryBytes = 2 * 1024 * 1024 * 1024
			request.Manifest.Resources.PIDs = 16
			request.Manifest.TimeoutSeconds = 10
			started := time.Now()
			result, err := backend.Run(t.Context(), request)
			t.Logf("elapsed=%s exit=%d timed_out=%t cleanup=%t/%t err=%v stdout=%q stderr=%q", time.Since(started), result.ExitCode, result.TimedOut, result.TreeReaped, result.ACLsRestored, err, result.Stdout.Data, result.Stderr.Data)
			if err != nil || result.ExitCode != 0 || !result.TreeReaped || !result.ACLsRestored || !strings.Contains(string(result.Stdout.Data), entry.marker) {
				t.Errorf("Node %s failed in LPAC", entry.name)
			}
		})
	}
	t.Run("inherited-child-boundaries", func(t *testing.T) {
		private := filepath.Join(base, "private", "sentinel.txt")
		if err := os.MkdirAll(filepath.Dir(private), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(private, []byte("synthetic host sentinel"), 0600); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		pathJSON, _ := json.Marshal(private)
		childSource := fmt.Sprintf(`const fs=require('node:fs');try{fs.readFileSync(%s);process.exit(2)}catch(e){if(!['EPERM','EACCES'].includes(e.code))throw e;console.log('child-private-denied')}const timer=setTimeout(()=>process.exit(3),1500);const s=require('node:net').createConnection({host:'127.0.0.1',port:%d});s.on('connect',()=>process.exit(4));s.on('error',()=>{clearTimeout(timer);console.log('child-network-denied')})`, pathJSON, listener.Addr().(*net.TCPAddr).Port)
		childJSON, _ := json.Marshal(childSource)
		parentSource := fmt.Sprintf(`const c=require('node:child_process').spawn(process.execPath,['-e',%s],{stdio:'inherit'});c.on('error',e=>{console.error(e);process.exitCode=1});c.on('close',code=>{process.exitCode=code})`, childJSON)
		request := localWindowsTestRequest(t, backend, drydock, toolchain, "/node", "/node/node.exe", []string{"-e", parentSource})
		request.Manifest.Resources.MemoryBytes = 2 * 1024 * 1024 * 1024
		request.Manifest.Resources.PIDs = 16
		request.Manifest.TimeoutSeconds = 10
		result, err := backend.Run(t.Context(), request)
		if err != nil || result.ExitCode != 0 || !result.TreeReaped || !result.ACLsRestored ||
			!strings.Contains(string(result.Stdout.Data), "child-private-denied") || !strings.Contains(string(result.Stdout.Data), "child-network-denied") {
			t.Fatalf("inherited Node LPAC child lost its boundaries: err=%v stdout=%s stderr=%s", err, result.Stdout.Data, result.Stderr.Data)
		}
		if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(25 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if connection, err := listener.Accept(); err == nil {
			connection.Close()
			t.Fatal("LPAC child reached host loopback listener")
		}
	})
}
