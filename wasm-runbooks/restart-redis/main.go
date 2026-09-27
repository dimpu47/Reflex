package main

import (
	"fmt"
	"os"
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
	fmt.Println("Attempting to restart Redis via Wasm sandbox...")
	// Try to execute a host function call!
	execHostCommand("kubectl rollout restart deploy/redis")
	
	fmt.Println("Error: Redis cluster quorum not met. Restart failed!")
	os.Exit(1)
}
