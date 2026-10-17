package main

import "C"

func main() {}

// lifetime_go_payload is referenced by the hybrid C shell (built with
// -DLIFETIME_EMBEDS_GO) to pull this Go archive — and its runtime — into the
// resulting dynamic library. Returning a constant keeps the payload free of
// any cgo dependency on the shell.

//export lifetime_go_payload
func lifetime_go_payload() C.int { return 42 }
