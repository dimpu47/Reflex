//go:build wasip1
package main
import (
	"fmt"
	"os"
	"unsafe"
)
//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32

func execHostCommand(cmd string) uint32 {
	if len(cmd) == 0 { return 0 }
	b := []byte(cmd)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	return kubectlExec(ptr, uint32(len(b)))
}

func main() {
	fmt.Println("Restoring connections by scaling Redis deployment back to 1 replica...")
	rc := execHostCommand("kubectl scale deploy/redis --replicas=1")
	if rc != 0 {
		fmt.Println("ERROR: host refused or failed the scale up request (code", rc, ")")
		os.Exit(int(rc))
	}
	fmt.Println("OK: deploy/redis client connections restored (scaled to 1).")
}
