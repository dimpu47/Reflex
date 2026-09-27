package main

import (
	"fmt"
	"unsafe"
)

//go:wasmimport env kubectl_exec
func kubectl_exec(ptr uint32, length uint32) uint32

func execHostCommand(cmd string) uint32 {
	if len(cmd) == 0 {
		return 0
	}
	b := []byte(cmd)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	return kubectl_exec(ptr, uint32(len(b)))
}

func main() {
	fmt.Println("Restarting Sample App Deployment in Kubernetes securely from Wasm sandbox...")
	execHostCommand("kubectl rollout restart deploy/sample-app")
	fmt.Println("Action: Kubectl Rollout Restart executed via Host Function.")
}
