//go:build wasip1

// Command restart-redis is a sandboxed Wasm runbook that asks the host to
// perform a rollout restart of the redis Deployment.
//
// It has no filesystem, network, or shell access of its own. The only thing
// it can do is call the `env.kubectl_exec` host import, and the host decides
// whether that is permitted (OPA policy plus a verb allowlist) and runs it.
package main

import (
	"fmt"
	"os"
	"unsafe"
)

//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32

// execHostCommand copies cmd into guest linear memory and hands the pointer
// to the host. The host reads the bytes back out via mod.Memory().Read.
func execHostCommand(cmd string) uint32 {
	if len(cmd) == 0 {
		return 0
	}
	b := []byte(cmd)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	return kubectlExec(ptr, uint32(len(b)))
}

func main() {
	fmt.Println("Requesting rollout restart of deploy/redis via host function...")
	rc := execHostCommand("kubectl rollout restart deploy/redis")
	if rc != 0 {
		fmt.Println("ERROR: host refused or failed the restart request (code", rc, ")")
		os.Exit(int(rc))
	}
	fmt.Println("OK: deploy/redis rollout restart executed by host.")
}
