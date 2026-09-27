//go:build wasip1

// Command restart-deployment restarts the sample-app Deployment through the
// env.kubectl_exec host import. See restart-redis for the same pattern.
package main

import (
	"fmt"
	"os"
	"unsafe"
)

//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32

func execHostCommand(cmd string) uint32 {
	if len(cmd) == 0 {
		return 0
	}
	b := []byte(cmd)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	return kubectlExec(ptr, uint32(len(b)))
}

func main() {
	fmt.Println("Requesting rollout restart of deploy/sample-app via host function...")
	rc := execHostCommand("kubectl rollout restart deploy/sample-app")
	if rc != 0 {
		fmt.Println("ERROR: host refused or failed the restart request (code", rc, ")")
		os.Exit(int(rc))
	}
	fmt.Println("OK: deploy/sample-app rollout restart executed by host.")
}
